package workflow

import (
	"sort"
	"strings"
)

func ValidateRealRun(workflow Workflow, providers ProviderRegistry) []Issue {
	registered := realProviderNames(providers)
	var issues []Issue
	if workflow.Provider != "" && !registered[workflow.Provider] {
		issues = append(issues, realProviderIssue("", workflow.Provider, registered))
	}
	if workflow.Interactive != nil {
		issues = append(issues, ignoredRealFieldIssue("", "interactive"))
	}
	if workflow.Worktree != nil {
		issues = append(issues, ignoredRealFieldIssue("", "worktree"))
	}

	for _, node := range workflow.Nodes {
		kind := node.Kind()
		switch kind {
		case "prompt", "command", "bash":
		default:
			issues = append(issues, Issue{
				Level:   IssueError,
				NodeID:  node.ID,
				Field:   kind,
				Message: "node " + node.ID + " uses unsupported real node kind " + kind + "; real mode supports prompt, command, and bash",
			})
		}
		if node.Provider != "" && !registered[node.Provider] {
			issues = append(issues, realProviderIssue(node.ID, node.Provider, registered))
		}
		// Real preflight prevents authored execution controls from being silently ignored.
		for _, field := range ignoredRealNodeFields(node) {
			issues = append(issues, ignoredRealFieldIssue(node.ID, field))
		}
	}
	return issues
}

func realProviderNames(providers ProviderRegistry) map[string]bool {
	if providers == nil {
		providers = ProviderRegistry{"opencode": nil}
	}
	registered := map[string]bool{}
	for name := range providers {
		if strings.TrimSpace(name) != "" {
			registered[name] = true
		}
	}
	return registered
}

func realProviderIssue(nodeID string, provider string, registered map[string]bool) Issue {
	return Issue{
		Level:   IssueError,
		NodeID:  nodeID,
		Field:   "provider",
		Message: "provider " + provider + " was not registered for real runs; registered providers: " + strings.Join(sortedProviderNames(registered), ", "),
	}
}

func ignoredRealFieldIssue(nodeID string, field string) Issue {
	message := "field " + field + " is not executed in real mode; remove it before starting a real run"
	if nodeID != "" {
		message = "node " + nodeID + " field " + field + " is not executed in real mode; remove it before starting a real run"
	}
	return Issue{
		Level:   IssueError,
		NodeID:  nodeID,
		Field:   field,
		Message: message,
	}
}

func ignoredRealNodeFields(node Node) []string {
	var fields []string
	if node.When != "" {
		fields = append(fields, "when")
	}
	if node.Retry != nil {
		fields = append(fields, "retry")
	}
	if node.Hooks != nil {
		fields = append(fields, "hooks")
	}
	if node.MCP != "" {
		fields = append(fields, "mcp")
	}
	if len(node.Skills) > 0 {
		fields = append(fields, "skills")
	}
	if len(node.AllowedTools) > 0 {
		fields = append(fields, "allowed_tools")
	}
	return fields
}

func sortedProviderNames(registered map[string]bool) []string {
	names := make([]string, 0, len(registered))
	for name := range registered {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
