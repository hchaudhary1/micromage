package workflow

import (
	"strings"

	"gopkg.in/yaml.v3"
)

func ParseYAML(input string) (Workflow, []Issue) {
	var workflow Workflow
	if strings.TrimSpace(input) == "" {
		return workflow, []Issue{{Level: IssueError, Field: "yaml", Message: "workflow YAML cannot be empty"}}
	}
	if err := yaml.Unmarshal([]byte(input), &workflow); err != nil {
		return workflow, []Issue{{Level: IssueError, Field: "yaml", Message: err.Error()}}
	}
	return workflow, Validate(workflow)
}

func Validate(workflow Workflow) []Issue {
	var issues []Issue
	issues = append(issues, workflow.InvalidTypes...)
	if workflow.Name == "" {
		issues = append(issues, Issue{Level: IssueError, Field: "name", Message: "name is required"})
	}
	if workflow.Description == "" {
		issues = append(issues, Issue{Level: IssueError, Field: "description", Message: "description is required"})
	}
	if len(workflow.Nodes) == 0 {
		issues = append(issues, Issue{Level: IssueError, Field: "nodes", Message: "at least one node is required"})
	}

	seen := map[string]bool{}
	for _, node := range workflow.Nodes {
		issues = append(issues, node.typeIssues...)
		if node.ID == "" {
			issues = append(issues, Issue{Level: IssueError, Field: "id", Message: "node id is required"})
			continue
		}
		if seen[node.ID] {
			issues = append(issues, Issue{Level: IssueError, NodeID: node.ID, Field: "id", Message: "duplicate node id"})
		}
		seen[node.ID] = true
	}

	for _, node := range workflow.Nodes {
		if node.ID == "" {
			continue
		}
		issues = append(issues, validateNode(node)...)
		for _, dep := range node.DependsOn {
			if !seen[dep] {
				issues = append(issues, Issue{Level: IssueError, NodeID: node.ID, Field: "depends_on", Message: "dependency " + dep + " was not found"})
			}
		}
	}

	if HasCycle(workflow.Nodes) {
		issues = append(issues, Issue{Level: IssueError, Field: "depends_on", Message: "workflow graph contains a cycle"})
	}
	if hasRuntimeMetadata(workflow) {
		// Runtime-aware warnings keep authoring honest while v1 remains a UI shell.
		issues = append(issues, Issue{Level: IssueWarning, Field: "runtime", Message: "runtime fields are displayed but not executed in v1"})
	}
	return issues
}

func validateNode(node Node) []Issue {
	var issues []Issue
	kinds := node.kindFields()
	switch len(kinds) {
	case 0:
		issues = append(issues, Issue{Level: IssueError, NodeID: node.ID, Message: "node must have one executable field"})
	case 1:
		issues = append(issues, validateExecutableContent(node, kinds[0])...)
	default:
		issues = append(issues, Issue{Level: IssueError, NodeID: node.ID, Message: "node must have only one executable field"})
	}
	if node.TriggerRule != "" && !IsTriggerRule(node.TriggerRule) {
		issues = append(issues, Issue{Level: IssueError, NodeID: node.ID, Field: "trigger_rule", Message: "invalid trigger rule"})
	}
	if node.Retry != nil {
		if attempts, ok := numberAsInt(node.Retry["max_attempts"]); ok && attempts <= 0 {
			issues = append(issues, Issue{Level: IssueError, NodeID: node.ID, Field: "retry.max_attempts", Message: "retry max_attempts must be positive"})
		}
	}
	return issues
}

func validateExecutableContent(node Node, kind string) []Issue {
	switch kind {
	case "command":
		if node.Command == "" {
			return []Issue{{Level: IssueError, NodeID: node.ID, Field: "command", Message: "command cannot be empty"}}
		}
	case "prompt":
		if node.Prompt == "" {
			return []Issue{{Level: IssueError, NodeID: node.ID, Field: "prompt", Message: "prompt cannot be empty"}}
		}
	case "bash":
		if node.Bash == "" {
			return []Issue{{Level: IssueError, NodeID: node.ID, Field: "bash", Message: "bash script cannot be empty"}}
		}
	case "script":
		if isEmptyValue(node.Script) {
			return []Issue{{Level: IssueError, NodeID: node.ID, Field: "script", Message: "script cannot be empty"}}
		}
	case "loop":
		// Executable nodes need real payloads so previews do not bless placeholders.
		if isEmptyValue(node.Loop) {
			return []Issue{{Level: IssueError, NodeID: node.ID, Field: "loop", Message: "loop cannot be empty"}}
		}
	case "approval":
		if isEmptyValue(node.Approval) {
			return []Issue{{Level: IssueError, NodeID: node.ID, Field: "approval", Message: "approval cannot be empty"}}
		}
	case "cancel":
		if node.Cancel == "" {
			return []Issue{{Level: IssueError, NodeID: node.ID, Field: "cancel", Message: "cancel reason cannot be empty"}}
		}
	}
	return nil
}

func numberAsInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
}

func isEmptyValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(typed) == ""
	case map[string]any:
		return len(typed) == 0
	case []any:
		return len(typed) == 0
	default:
		return false
	}
}

func hasRuntimeMetadata(workflow Workflow) bool {
	if workflow.Provider != "" || workflow.Model != "" || workflow.Worktree != nil {
		return true
	}
	for _, node := range workflow.Nodes {
		if node.Provider != "" || node.Model != "" || node.Context != "" || node.Agent != "" || node.IdleTimeout != nil || len(node.AllowedTools) > 0 || len(node.Outputs) > 0 || node.Retry != nil || node.Hooks != nil || node.MCP != "" || len(node.Skills) > 0 || node.When != "" || node.Kind() == "approval" || node.Kind() == "loop" || node.Kind() == "script" {
			return true
		}
	}
	return false
}

func HasErrors(issues []Issue) bool {
	for _, issue := range issues {
		if issue.Level == IssueError {
			return true
		}
	}
	return false
}
