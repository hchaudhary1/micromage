package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	definitionStoreSchemaVersion = 1

	DefinitionSourceEmbedded = "embedded"
	DefinitionSourceProject  = "project"

	DefinitionKindWorkflow = "workflow"
	DefinitionKindTemplate = "template"
)

type DefinitionStore struct {
	repoRoot  string
	storeRoot string
	now       func() time.Time
	rename    func(string, string) error
	audit     *AuditStore
}

type definitionIndex struct {
	SchemaVersion int                    `json:"schema_version"`
	Items         []definitionIndexEntry `json:"items"`
}

type definitionIndexEntry struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Description      string    `json:"description"`
	Source           string    `json:"source"`
	Kind             string    `json:"kind"`
	Path             string    `json:"path"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	Valid            bool      `json:"valid"`
	ValidationStatus string    `json:"validation_status"`
	Issues           []Issue   `json:"issues,omitempty"`
}

func NewDefinitionStore(repoRoot string) *DefinitionStore {
	if repoRoot == "" {
		repoRoot = "."
	}
	cleanRoot := filepath.Clean(repoRoot)
	return &DefinitionStore{
		repoRoot:  cleanRoot,
		storeRoot: filepath.Join(cleanRoot, ".micromage"),
		now:       func() time.Time { return time.Now().UTC() },
		rename:    os.Rename,
		audit:     NewAuditStore(cleanRoot),
	}
}

func (store *DefinitionStore) SaveWorkflow(id string, yamlText string) (Template, error) {
	return store.saveDefinition(DefinitionKindWorkflow, id, yamlText)
}

func (store *DefinitionStore) SaveTemplate(id string, yamlText string) (Template, error) {
	return store.saveDefinition(DefinitionKindTemplate, id, yamlText)
}

func (store *DefinitionStore) DiscoverDefinitions(embeddedWorkflows []Template, embeddedTemplates []Template) ([]Template, error) {
	projectWorkflows, err := store.loadProjectDefinitions(DefinitionKindWorkflow)
	if err != nil {
		return nil, err
	}
	projectTemplates, err := store.loadProjectDefinitions(DefinitionKindTemplate)
	if err != nil {
		return nil, err
	}

	overrides := map[string]bool{}
	for _, item := range projectWorkflows {
		overrides[item.ID] = true
	}
	for _, item := range projectTemplates {
		overrides[item.ID] = true
	}

	items := make([]Template, 0, len(embeddedWorkflows)+len(embeddedTemplates)+len(projectWorkflows)+len(projectTemplates))
	items = appendEmbeddedDefinitions(items, embeddedWorkflows, DefinitionKindWorkflow, overrides)
	items = appendEmbeddedDefinitions(items, embeddedTemplates, DefinitionKindTemplate, overrides)
	items = append(items, projectWorkflows...)
	items = append(items, projectTemplates...)
	return items, nil
}

func (store *DefinitionStore) saveDefinition(kind string, id string, yamlText string) (Template, error) {
	if store == nil {
		return Template{}, errors.New("definition store is nil")
	}
	id = strings.TrimSpace(id)
	if !validDefinitionID(id) {
		return Template{}, errors.New("definition id must match " + nodeIDPattern)
	}
	if !isDefinitionKind(kind) {
		return Template{}, fmt.Errorf("unknown definition kind %q", kind)
	}
	preview := BuildPreview(yamlText)
	if HasErrors(preview.Issues) {
		return Template{}, fmt.Errorf("definition YAML is invalid: %s", validationSummary(preview.Issues))
	}
	dir := store.dirForKind(kind)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Template{}, fmt.Errorf("create definition directory: %w", err)
	}
	index, err := store.readIndex(kind)
	if err != nil {
		return Template{}, err
	}

	now := store.now().UTC()
	position, found := findDefinitionEntry(index.Items, id)
	createdAt := now
	if found {
		createdAt = index.Items[position].CreatedAt
	}
	relativePath := filepath.Join(".micromage", definitionDirName(kind), id+".yaml")
	entry := definitionIndexEntry{
		ID:               id,
		Name:             preview.Workflow.Name,
		Description:      preview.Workflow.Description,
		Source:           DefinitionSourceProject,
		Kind:             kind,
		Path:             relativePath,
		CreatedAt:        createdAt,
		UpdatedAt:        now,
		Valid:            preview.CanRun,
		ValidationStatus: validationStatus(preview.CanRun),
		Issues:           preview.Issues,
	}

	filePath := filepath.Join(dir, id+".yaml")
	oldContent, oldErr := os.ReadFile(filePath)
	existed := oldErr == nil
	if oldErr != nil && !errors.Is(oldErr, os.ErrNotExist) {
		return Template{}, fmt.Errorf("read existing definition: %w", oldErr)
	}
	if existed {
		if err := os.WriteFile(filePath+".bak", oldContent, 0o644); err != nil {
			return Template{}, fmt.Errorf("write definition backup: %w", err)
		}
	}

	tmpPath := filePath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(yamlText), 0o644); err != nil {
		return Template{}, fmt.Errorf("write temporary definition: %w", err)
	}
	// Atomic YAML replacement lets users recover either the old or new definition after interruption.
	if err := store.rename(tmpPath, filePath); err != nil {
		_ = os.Remove(tmpPath)
		return Template{}, fmt.Errorf("replace definition: %w", err)
	}

	if found {
		index.Items[position] = entry
	} else {
		index.Items = append(index.Items, entry)
	}
	sortDefinitionEntries(index.Items)
	if err := store.writeIndex(kind, index); err != nil {
		if existed {
			_ = os.WriteFile(filePath, oldContent, 0o644)
		} else {
			_ = os.Remove(filePath)
		}
		return Template{}, err
	}
	if err := store.auditDefinitionSaved(kind, entry); err != nil {
		return Template{}, err
	}
	return templateFromIndexEntry(entry, yamlText), nil
}

func (store *DefinitionStore) auditDefinitionSaved(kind string, entry definitionIndexEntry) error {
	if store.audit == nil {
		return nil
	}
	eventType := AuditTypeWorkflowSaved
	if kind == DefinitionKindTemplate {
		eventType = AuditTypeTemplateSaved
	}
	// Saved-definition audits prove user-managed content changed without storing YAML bodies.
	return store.audit.Append(AuditEvent{
		Type:       eventType,
		WorkflowID: entry.ID,
		Actor:      AuditActorLocalBrowser,
		Outcome:    "success",
		Details: map[string]string{
			"kind":   kind,
			"path":   entry.Path,
			"source": entry.Source,
		},
	})
}

func (store *DefinitionStore) loadProjectDefinitions(kind string) ([]Template, error) {
	if store == nil {
		return nil, errors.New("definition store is nil")
	}
	dir := store.dirForKind(kind)
	paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, fmt.Errorf("glob definitions: %w", err)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return nil, nil
	}

	index, err := store.readIndex(kind)
	if err != nil {
		return nil, err
	}
	indexByID := map[string]definitionIndexEntry{}
	for _, entry := range index.Items {
		indexByID[entry.ID] = entry
	}

	items := make([]Template, 0, len(paths))
	for _, itemPath := range paths {
		id := strings.TrimSuffix(filepath.Base(itemPath), filepath.Ext(itemPath))
		if !validDefinitionID(id) {
			continue
		}
		content, err := os.ReadFile(itemPath)
		if err != nil {
			return nil, fmt.Errorf("read definition %s: %w", id, err)
		}
		preview := BuildPreview(string(content))
		entry := indexByID[id]
		entry.ID = id
		entry.Source = DefinitionSourceProject
		entry.Kind = kind
		entry.Path = store.relativeToRepo(itemPath)
		entry.Valid = preview.CanRun
		entry.ValidationStatus = validationStatus(preview.CanRun)
		entry.Issues = preview.Issues
		if preview.Workflow.Name != "" {
			entry.Name = preview.Workflow.Name
		}
		if preview.Workflow.Description != "" {
			entry.Description = preview.Workflow.Description
		}
		if entry.Name == "" {
			entry.Name = id
		}
		items = append(items, templateFromIndexEntry(entry, string(content)))
	}
	return items, nil
}

func appendEmbeddedDefinitions(items []Template, embedded []Template, kind string, overrides map[string]bool) []Template {
	for _, item := range embedded {
		if overrides[item.ID] {
			continue
		}
		if item.Source == "" {
			item.Source = DefinitionSourceEmbedded
		}
		if item.Kind == "" {
			item.Kind = kind
		}
		if item.Issues == nil {
			preview := BuildPreview(item.YAML)
			item.Valid = preview.CanRun
			item.ValidationStatus = validationStatus(preview.CanRun)
			item.Issues = preview.Issues
			if item.Name == "" {
				item.Name = preview.Workflow.Name
			}
			if item.Description == "" {
				item.Description = preview.Workflow.Description
			}
		}
		if item.ValidationStatus == "" {
			item.ValidationStatus = validationStatus(item.Valid)
		}
		items = append(items, item)
	}
	return items
}

func (store *DefinitionStore) readIndex(kind string) (definitionIndex, error) {
	path := filepath.Join(store.dirForKind(kind), "index.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return definitionIndex{SchemaVersion: definitionStoreSchemaVersion}, nil
	}
	if err != nil {
		return definitionIndex{}, fmt.Errorf("read definition index: %w", err)
	}
	var index definitionIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return definitionIndex{}, fmt.Errorf("decode definition index: %w", err)
	}
	if index.SchemaVersion != definitionStoreSchemaVersion {
		return definitionIndex{}, fmt.Errorf("unsupported definition index schema version %d", index.SchemaVersion)
	}
	return index, nil
}

func (store *DefinitionStore) writeIndex(kind string, index definitionIndex) error {
	index.SchemaVersion = definitionStoreSchemaVersion
	sortDefinitionEntries(index.Items)
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return fmt.Errorf("encode definition index: %w", err)
	}
	data = append(data, '\n')
	dir := store.dirForKind(kind)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create definition index directory: %w", err)
	}
	tmpPath := filepath.Join(dir, "index.json.tmp")
	indexPath := filepath.Join(dir, "index.json")
	// Atomic index rewrites keep saved-workflow lists inspectable after a crash.
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("write temporary definition index: %w", err)
	}
	if err := store.rename(tmpPath, indexPath); err != nil {
		return fmt.Errorf("replace definition index: %w", err)
	}
	return nil
}

func (store *DefinitionStore) dirForKind(kind string) string {
	return filepath.Join(store.storeRoot, definitionDirName(kind))
}

func (store *DefinitionStore) relativeToRepo(path string) string {
	if path == "" {
		return ""
	}
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		clean = filepath.Clean(filepath.Join(store.repoRoot, clean))
	}
	rel, err := filepath.Rel(store.repoRoot, clean)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Clean(path)
}

func definitionDirName(kind string) string {
	if kind == DefinitionKindTemplate {
		return "templates"
	}
	return "workflows"
}

func isDefinitionKind(kind string) bool {
	return kind == DefinitionKindWorkflow || kind == DefinitionKindTemplate
}

func validDefinitionID(id string) bool {
	return validNodeID(id)
}

func findDefinitionEntry(items []definitionIndexEntry, id string) (int, bool) {
	for index, item := range items {
		if item.ID == id {
			return index, true
		}
	}
	return 0, false
}

func sortDefinitionEntries(items []definitionIndexEntry) {
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].ID < items[j].ID
	})
}

func templateFromIndexEntry(entry definitionIndexEntry, yamlText string) Template {
	return Template{
		ID:               entry.ID,
		Name:             entry.Name,
		Description:      entry.Description,
		YAML:             yamlText,
		Source:           entry.Source,
		Kind:             entry.Kind,
		Path:             entry.Path,
		CreatedAt:        entry.CreatedAt,
		UpdatedAt:        entry.UpdatedAt,
		Valid:            entry.Valid,
		ValidationStatus: entry.ValidationStatus,
		Issues:           entry.Issues,
	}
}

func validationStatus(valid bool) string {
	if valid {
		return "valid"
	}
	return "invalid"
}

func validationSummary(issues []Issue) string {
	var messages []string
	for _, issue := range issues {
		if issue.Level != IssueError {
			continue
		}
		message := issue.Message
		if issue.NodeID != "" {
			message = issue.NodeID + ": " + message
		}
		if issue.Field != "" {
			message = issue.Field + ": " + message
		}
		messages = append(messages, message)
	}
	if len(messages) == 0 {
		return "unknown validation error"
	}
	return strings.Join(messages, "; ")
}
