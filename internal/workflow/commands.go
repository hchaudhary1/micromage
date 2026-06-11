package workflow

import (
	"io/fs"
	"path"
	"sort"
	"strings"
)

type Command struct {
	ID           string `json:"id"`
	Description  string `json:"description,omitempty"`
	ArgumentHint string `json:"argument_hint,omitempty"`
	Body         string `json:"body"`
}

type CommandRegistry map[string]Command

func LoadCommands(source fs.FS, pattern string) ([]Command, error) {
	paths, err := fs.Glob(source, pattern)
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)

	commands := make([]Command, 0, len(paths))
	for _, commandPath := range paths {
		content, err := fs.ReadFile(source, commandPath)
		if err != nil {
			return nil, err
		}
		id := strings.TrimSuffix(path.Base(commandPath), path.Ext(commandPath))
		frontmatter, body := splitFrontmatter(string(content))
		commands = append(commands, Command{
			ID:           id,
			Description:  frontmatter["description"],
			ArgumentHint: frontmatter["argument-hint"],
			Body:         strings.TrimSpace(body),
		})
	}
	return commands, nil
}

func NewCommandRegistry(commands []Command) CommandRegistry {
	registry := CommandRegistry{}
	for _, command := range commands {
		registry[command.ID] = command
	}
	return registry
}

func ValidateCommands(workflow Workflow, registry CommandRegistry) []Issue {
	if registry == nil {
		return nil
	}
	var issues []Issue
	for _, node := range workflow.Nodes {
		if node.Kind() != "command" || node.Command == "" {
			continue
		}
		if _, ok := registry[node.Command]; !ok {
			// Command validation prevents real runs from starting with missing prompts.
			issues = append(issues, Issue{Level: IssueError, NodeID: node.ID, Field: "command", Message: "command " + node.Command + " was not found"})
		}
	}
	return issues
}

func splitFrontmatter(input string) (map[string]string, string) {
	out := map[string]string{}
	trimmed := strings.TrimPrefix(input, "\ufeff")
	if !strings.HasPrefix(trimmed, "---\n") {
		return out, input
	}
	rest := strings.TrimPrefix(trimmed, "---\n")
	front, body, ok := strings.Cut(rest, "\n---")
	if !ok {
		return out, input
	}
	for _, line := range strings.Split(front, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		out[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	body = strings.TrimPrefix(body, "\n")
	return out, body
}
