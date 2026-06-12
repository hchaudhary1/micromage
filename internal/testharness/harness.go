package testharness

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hchaudhary1/micromage/internal/runlog"
)

const LiveOpenCodeEnv = "MICROMAGE_OPENCODE_LIVE"

func WorkflowFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		paths = append(paths, filepath.Join(dir, entry.Name()))
	}
	sort.Strings(paths)
	return paths, nil
}

func ComplexWorkflowDir(repoRoot string) string {
	return filepath.Join(repoRoot, "testdata", "workflows", "complex")
}

func ReferenceDefaultsDir() string {
	if dir := os.Getenv("MICROMAGE_REFERENCE_DEFAULTS"); dir != "" {
		return dir
	}
	return filepath.Join(repoRootFromCwd(), "assets", "defaults", "workflows")
}

func DefaultCommandsDir() string {
	if dir := os.Getenv("MICROMAGE_DEFAULT_COMMANDS"); dir != "" {
		return dir
	}
	return filepath.Join(repoRootFromCwd(), "assets", "defaults", "commands")
}

func repoRootFromCwd() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}

func OpenCodeModel() string {
	if model := os.Getenv("MICROMAGE_OPENCODE_MODEL"); model != "" {
		return model
	}
	return "opencode/deepseek-v4-flash-free"
}

func DecodeJSONLEvents(path string) ([]runlog.Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var events []runlog.Event
	for line := 1; scanner.Scan(); line++ {
		var event runlog.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("decode event log line %d: %w", line, err)
		}
		if event.Type == "" {
			return nil, fmt.Errorf("event log line %d is missing type", line)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read event log: %w", err)
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("event log is empty")
	}
	return events, nil
}

func RequireCompletedEventLog(events []runlog.Event, requiredNode string) error {
	var started, passed, nodeStarted, nodePassed, sawOutput bool
	for _, event := range events {
		switch event.Type {
		case runlog.EventWorkflowStarted:
			started = true
		case runlog.EventWorkflowPassed:
			passed = true
		case runlog.EventNodeStarted:
			nodeStarted = nodeStarted || event.NodeID == requiredNode
		case runlog.EventNodePassed:
			nodePassed = nodePassed || event.NodeID == requiredNode
		case runlog.EventNodeOutput:
			sawOutput = sawOutput || event.NodeID == requiredNode
		}
	}
	if !started || !passed || !nodeStarted || !nodePassed || !sawOutput {
		return fmt.Errorf("event log missing required lifecycle events for %q", requiredNode)
	}
	return nil
}

func SanitizeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
