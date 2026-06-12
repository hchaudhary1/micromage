package workflow

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const savedWorkflowYAML = `name: Saved Workflow
description: Saved workflow
nodes:
  - id: saved
    prompt: Save this workflow.
`

func TestDefinitionStoreDiscoverOrdersProjectItemsAndOverridesEmbedded(t *testing.T) {
	repo := t.TempDir()
	store := NewDefinitionStore(repo)
	store.now = fixedClock(time.Date(2026, 6, 12, 4, 0, 0, 0, time.UTC))

	if _, err := store.SaveWorkflow("shared", `name: Project Shared
description: Project shared
nodes:
  - id: project
    prompt: Project wins.
`); err != nil {
		t.Fatalf("SaveWorkflow shared returned error: %v", err)
	}
	if _, err := store.SaveWorkflow("project-flow", savedWorkflowYAML); err != nil {
		t.Fatalf("SaveWorkflow project-flow returned error: %v", err)
	}
	if _, err := store.SaveTemplate("project-template", `name: Project Template
description: Project template
nodes:
  - id: template
    prompt: Reuse this template.
`); err != nil {
		t.Fatalf("SaveTemplate returned error: %v", err)
	}

	items, err := store.DiscoverDefinitions([]Template{
		{ID: "embedded-only", Name: "Embedded Only", Description: "Embedded", YAML: savedWorkflowYAML, Source: DefinitionSourceEmbedded, Kind: DefinitionKindWorkflow, Valid: true},
		{ID: "shared", Name: "Embedded Shared", Description: "Embedded shared", YAML: savedWorkflowYAML, Source: DefinitionSourceEmbedded, Kind: DefinitionKindWorkflow, Valid: true},
	}, nil)
	if err != nil {
		t.Fatalf("DiscoverDefinitions returned error: %v", err)
	}

	got := templateIDsWithSource(items)
	want := []string{
		"embedded-only:embedded:workflow",
		"project-flow:project:workflow",
		"shared:project:workflow",
		"project-template:project:template",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("expected discovery order %v, got %v", want, got)
	}
	if items[2].Name != "Project Shared" || !items[2].Valid {
		t.Fatalf("expected project definition to replace embedded shared item, got %#v", items[2])
	}
}

func TestDefinitionStoreRejectsInvalidYAMLWithoutOverwriting(t *testing.T) {
	repo := t.TempDir()
	store := NewDefinitionStore(repo)
	store.now = fixedClock(time.Date(2026, 6, 12, 5, 0, 0, 0, time.UTC))
	if _, err := store.SaveWorkflow("valid", savedWorkflowYAML); err != nil {
		t.Fatalf("SaveWorkflow returned error: %v", err)
	}

	_, err := store.SaveWorkflow("valid", `name: Broken
description: Broken workflow
nodes:
  - id: broken
`)
	if err == nil {
		t.Fatal("expected invalid YAML to be rejected")
	}
	if !strings.Contains(err.Error(), "node must have one executable field") {
		t.Fatalf("expected validation detail, got %v", err)
	}
	data, err := os.ReadFile(filepath.Join(repo, ".micromage", "workflows", "valid.yaml"))
	if err != nil {
		t.Fatalf("read saved workflow: %v", err)
	}
	if string(data) != savedWorkflowYAML {
		t.Fatalf("invalid update should leave workflow unchanged, got %q", string(data))
	}
	if _, err := os.Stat(filepath.Join(repo, ".micromage", "workflows", "valid.yaml.bak")); !os.IsNotExist(err) {
		t.Fatalf("validation failure should not create backup, stat error: %v", err)
	}
}

func TestDefinitionStoreOverwriteCreatesBackupAndUpdatesIndex(t *testing.T) {
	repo := t.TempDir()
	store := NewDefinitionStore(repo)
	times := []time.Time{
		time.Date(2026, 6, 12, 6, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 12, 6, 30, 0, 0, time.UTC),
	}
	store.now = func() time.Time {
		next := times[0]
		times = times[1:]
		return next
	}

	first, err := store.SaveTemplate("starter", savedWorkflowYAML)
	if err != nil {
		t.Fatalf("first SaveTemplate returned error: %v", err)
	}
	secondYAML := `name: Updated Template
description: Updated template
nodes:
  - id: updated
    prompt: Updated.
`
	second, err := store.SaveTemplate("starter", secondYAML)
	if err != nil {
		t.Fatalf("second SaveTemplate returned error: %v", err)
	}

	backup, err := os.ReadFile(filepath.Join(repo, ".micromage", "templates", "starter.yaml.bak"))
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(backup) != savedWorkflowYAML {
		t.Fatalf("expected backup to preserve first version, got %q", string(backup))
	}
	current, err := os.ReadFile(filepath.Join(repo, ".micromage", "templates", "starter.yaml"))
	if err != nil {
		t.Fatalf("read current template: %v", err)
	}
	if string(current) != secondYAML {
		t.Fatalf("expected current template to be updated, got %q", string(current))
	}
	if !second.CreatedAt.Equal(first.CreatedAt) || !second.UpdatedAt.After(first.UpdatedAt) {
		t.Fatalf("expected created_at to remain stable and updated_at to advance: first=%#v second=%#v", first, second)
	}

	index := readDefinitionIndexFile(t, repo, "templates")
	if len(index.Items) != 1 || index.Items[0].Name != "Updated Template" || !index.Items[0].Valid {
		t.Fatalf("expected updated template metadata in index, got %#v", index)
	}
}

func TestDefinitionStoreIndexRewriteIsAtomicOnRenameFailure(t *testing.T) {
	repo := t.TempDir()
	store := NewDefinitionStore(repo)
	store.now = fixedClock(time.Date(2026, 6, 12, 7, 0, 0, 0, time.UTC))
	if _, err := store.SaveWorkflow("first", savedWorkflowYAML); err != nil {
		t.Fatalf("initial SaveWorkflow returned error: %v", err)
	}
	before, err := os.ReadFile(filepath.Join(repo, ".micromage", "workflows", "index.json"))
	if err != nil {
		t.Fatalf("read initial index: %v", err)
	}
	store.rename = func(oldPath, newPath string) error {
		if filepath.Base(oldPath) == "index.json.tmp" {
			return errors.New("rename blocked")
		}
		return os.Rename(oldPath, newPath)
	}

	_, err = store.SaveWorkflow("second", `name: Second
description: Second workflow
nodes:
  - id: second
    prompt: Second.
`)
	if err == nil {
		t.Fatal("expected index rename error")
	}
	after, err := os.ReadFile(filepath.Join(repo, ".micromage", "workflows", "index.json"))
	if err != nil {
		t.Fatalf("read index after failed rewrite: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("index should remain unchanged after failed atomic rename\nbefore=%s\nafter=%s", before, after)
	}
	var decoded definitionIndex
	if err := json.Unmarshal(after, &decoded); err != nil {
		t.Fatalf("index should remain valid JSON after failed rewrite: %v", err)
	}
}

func templateIDsWithSource(items []Template) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.ID+":"+item.Source+":"+item.Kind)
	}
	return out
}

func readDefinitionIndexFile(t *testing.T, repo string, dir string) definitionIndex {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repo, ".micromage", dir, "index.json"))
	if err != nil {
		t.Fatalf("read definition index: %v", err)
	}
	var index definitionIndex
	if err := json.Unmarshal(data, &index); err != nil {
		t.Fatalf("decode definition index: %v", err)
	}
	return index
}
