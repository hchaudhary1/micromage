package workflow

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	IssueError   = "error"
	IssueWarning = "warning"
)

var triggerRules = map[string]struct{}{
	"all_success":                 {},
	"one_success":                 {},
	"none_failed_min_one_success": {},
	"all_done":                    {},
}

type Workflow struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	Provider     string         `json:"provider,omitempty"`
	Model        string         `json:"model,omitempty"`
	Tags         []string       `json:"tags,omitempty"`
	Interactive  *bool          `json:"interactive,omitempty"`
	Worktree     map[string]any `json:"worktree,omitempty"`
	Nodes        []Node         `json:"nodes"`
	Extra        map[string]any `json:"extra,omitempty"`
	InvalidTypes []Issue        `json:"-"`
}

type Node struct {
	ID           string         `json:"id"`
	DependsOn    []string       `json:"depends_on,omitempty"`
	When         string         `json:"when,omitempty"`
	TriggerRule  string         `json:"trigger_rule,omitempty"`
	Provider     string         `json:"provider,omitempty"`
	Model        string         `json:"model,omitempty"`
	Context      string         `json:"context,omitempty"`
	Agent        string         `json:"agent,omitempty"`
	Command      string         `json:"command,omitempty"`
	Prompt       string         `json:"prompt,omitempty"`
	Bash         string         `json:"bash,omitempty"`
	Script       any            `json:"script,omitempty"`
	Loop         any            `json:"loop,omitempty"`
	Approval     any            `json:"approval,omitempty"`
	Cancel       string         `json:"cancel,omitempty"`
	Timeout      *int           `json:"timeout,omitempty"`
	Retry        map[string]any `json:"retry,omitempty"`
	Hooks        map[string]any `json:"hooks,omitempty"`
	MCP          string         `json:"mcp,omitempty"`
	Skills       []string       `json:"skills,omitempty"`
	IdleTimeout  *int           `json:"idle_timeout,omitempty"`
	AllowedTools []string       `json:"allowed_tools,omitempty"`
	Extra        map[string]any `json:"extra,omitempty"`
	fields       map[string]bool
	typeIssues   []Issue
}

type Issue struct {
	Level   string `json:"level"`
	Field   string `json:"field,omitempty"`
	NodeID  string `json:"node_id,omitempty"`
	Message string `json:"message"`
}

func (w *Workflow) UnmarshalYAML(value *yaml.Node) error {
	type rawWorkflow struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
		Provider    string `yaml:"provider"`
		Model       string `yaml:"model"`
		Tags        any    `yaml:"tags"`
		Interactive any    `yaml:"interactive"`
		Worktree    any    `yaml:"worktree"`
		Nodes       []Node `yaml:"nodes"`
	}

	var raw rawWorkflow
	if err := value.Decode(&raw); err != nil {
		return err
	}

	extra, err := decodeMapping(value)
	if err != nil {
		return err
	}

	w.Name = strings.TrimSpace(raw.Name)
	w.Description = strings.TrimSpace(raw.Description)
	w.Provider = strings.TrimSpace(raw.Provider)
	w.Model = strings.TrimSpace(raw.Model)
	w.Tags = normalizeTags(raw.Tags)
	w.Interactive = boolPointer(raw.Interactive)
	w.Worktree = objectMap(raw.Worktree)
	w.Nodes = raw.Nodes
	w.Extra = removeKeys(extra, "name", "description", "provider", "model", "tags", "interactive", "worktree", "nodes")
	if raw.Tags != nil && w.Tags == nil {
		w.InvalidTypes = append(w.InvalidTypes, Issue{Level: IssueWarning, Field: "tags", Message: "tags must be an array of strings"})
	}
	if raw.Interactive != nil && w.Interactive == nil {
		w.InvalidTypes = append(w.InvalidTypes, Issue{Level: IssueWarning, Field: "interactive", Message: "interactive must be a boolean"})
	}
	if raw.Worktree != nil && w.Worktree == nil {
		w.InvalidTypes = append(w.InvalidTypes, Issue{Level: IssueWarning, Field: "worktree", Message: "worktree must be a mapping"})
	}
	return nil
}

func (n *Node) UnmarshalYAML(value *yaml.Node) error {
	type rawNode struct {
		ID           string         `yaml:"id"`
		DependsOn    []string       `yaml:"depends_on"`
		When         string         `yaml:"when"`
		TriggerRule  string         `yaml:"trigger_rule"`
		Provider     string         `yaml:"provider"`
		Model        string         `yaml:"model"`
		Context      string         `yaml:"context"`
		Agent        string         `yaml:"agent"`
		Command      string         `yaml:"command"`
		Prompt       string         `yaml:"prompt"`
		Bash         string         `yaml:"bash"`
		Script       any            `yaml:"script"`
		Loop         any            `yaml:"loop"`
		Approval     any            `yaml:"approval"`
		Cancel       string         `yaml:"cancel"`
		Timeout      *int           `yaml:"timeout"`
		Retry        map[string]any `yaml:"retry"`
		Hooks        map[string]any `yaml:"hooks"`
		MCP          string         `yaml:"mcp"`
		Skills       []string       `yaml:"skills"`
		IdleTimeout  *int           `yaml:"idle_timeout"`
		AllowedTools []string       `yaml:"allowed_tools"`
	}

	var raw rawNode
	if err := value.Decode(&raw); err != nil {
		var loose map[string]any
		if looseErr := value.Decode(&loose); looseErr != nil {
			return err
		}
		n.typeIssues = append(n.typeIssues, Issue{Level: IssueError, Message: fmt.Sprintf("node has malformed fields: %v", err)})
	}

	extra, err := decodeMapping(value)
	if err != nil {
		return err
	}
	fields := fieldSet(value)

	n.ID = strings.TrimSpace(raw.ID)
	n.DependsOn = trimStrings(raw.DependsOn)
	n.When = strings.TrimSpace(raw.When)
	n.TriggerRule = strings.TrimSpace(raw.TriggerRule)
	n.Provider = strings.TrimSpace(raw.Provider)
	n.Model = strings.TrimSpace(raw.Model)
	n.Context = strings.TrimSpace(raw.Context)
	n.Agent = strings.TrimSpace(raw.Agent)
	n.Command = strings.TrimSpace(raw.Command)
	n.Prompt = strings.TrimSpace(raw.Prompt)
	n.Bash = strings.TrimSpace(raw.Bash)
	n.Script = raw.Script
	n.Loop = raw.Loop
	n.Approval = raw.Approval
	n.Cancel = strings.TrimSpace(raw.Cancel)
	n.Timeout = raw.Timeout
	n.Retry = raw.Retry
	n.Hooks = raw.Hooks
	n.MCP = strings.TrimSpace(raw.MCP)
	n.Skills = trimStrings(raw.Skills)
	n.IdleTimeout = raw.IdleTimeout
	n.AllowedTools = trimStrings(raw.AllowedTools)
	n.Extra = removeKeys(extra, "id", "depends_on", "when", "trigger_rule", "provider", "model", "context", "agent", "command", "prompt", "bash", "script", "loop", "approval", "cancel", "timeout", "retry", "hooks", "mcp", "skills", "idle_timeout", "allowed_tools")
	n.fields = fields
	return nil
}

func (n Node) Kind() string {
	kinds := n.kindFields()
	if len(kinds) == 1 {
		return kinds[0]
	}
	return "unknown"
}

func (n Node) kindFields() []string {
	if n.fields == nil {
		return n.inferredKindFields()
	}
	var kinds []string
	for _, key := range []string{"command", "prompt", "bash", "script", "loop", "approval", "cancel"} {
		if n.fields[key] {
			kinds = append(kinds, key)
		}
	}
	return kinds
}

func (n Node) inferredKindFields() []string {
	var kinds []string
	if n.Command != "" {
		kinds = append(kinds, "command")
	}
	if n.Prompt != "" {
		kinds = append(kinds, "prompt")
	}
	if n.Bash != "" {
		kinds = append(kinds, "bash")
	}
	if !isEmptyValue(n.Script) {
		kinds = append(kinds, "script")
	}
	if !isEmptyValue(n.Loop) {
		kinds = append(kinds, "loop")
	}
	if !isEmptyValue(n.Approval) {
		kinds = append(kinds, "approval")
	}
	if n.Cancel != "" {
		kinds = append(kinds, "cancel")
	}
	return kinds
}

func (n Node) Label() string {
	switch n.Kind() {
	case "command":
		return n.Command
	case "prompt":
		return "Prompt"
	case "bash":
		return "Shell"
	case "script":
		return "Script"
	case "loop":
		return "Loop"
	case "approval":
		return "Approval"
	case "cancel":
		return "Cancel"
	default:
		if n.ID != "" {
			return n.ID
		}
		return "Node"
	}
}

func (n Node) ContentPreview() string {
	switch n.Kind() {
	case "command":
		return n.Command
	case "prompt":
		return firstLine(n.Prompt)
	case "bash":
		return firstLine(n.Bash)
	case "script":
		return summarizeValue(n.Script)
	case "loop":
		return summarizeValue(n.Loop)
	case "approval":
		return summarizeValue(n.Approval)
	case "cancel":
		return n.Cancel
	default:
		return ""
	}
}

func IsTriggerRule(value string) bool {
	_, ok := triggerRules[value]
	return ok
}

func TriggerRules() []string {
	rules := make([]string, 0, len(triggerRules))
	for rule := range triggerRules {
		rules = append(rules, rule)
	}
	sort.Strings(rules)
	return rules
}

func boolPointer(value any) *bool {
	v, ok := value.(bool)
	if !ok {
		return nil
	}
	return &v
}

func normalizeTags(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	var tags []string
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			continue
		}
		text = strings.TrimSpace(text)
		if text == "" || seen[text] {
			continue
		}
		seen[text] = true
		tags = append(tags, text)
	}
	return tags
}

func objectMap(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		return typed
	case map[any]any:
		out := map[string]any{}
		for key, value := range typed {
			text, ok := key.(string)
			if ok {
				out[text] = value
			}
		}
		return out
	default:
		return nil
	}
}

func trimStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func firstLine(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	line, _, _ := strings.Cut(value, "\n")
	return strings.TrimSpace(line)
}

func summarizeValue(value any) string {
	switch typed := value.(type) {
	case string:
		return firstLine(typed)
	case map[string]any:
		if prompt, ok := typed["prompt"].(string); ok {
			return firstLine(prompt)
		}
		return fmt.Sprintf("%d fields", len(typed))
	case []any:
		return fmt.Sprintf("%d items", len(typed))
	case nil:
		return ""
	default:
		return fmt.Sprint(typed)
	}
}

func decodeMapping(value *yaml.Node) (map[string]any, error) {
	out := map[string]any{}
	if value.Kind != yaml.MappingNode {
		return out, nil
	}
	for i := 0; i < len(value.Content); i += 2 {
		key := value.Content[i].Value
		var decoded any
		if err := value.Content[i+1].Decode(&decoded); err != nil {
			return nil, err
		}
		out[key] = decoded
	}
	return out, nil
}

func fieldSet(value *yaml.Node) map[string]bool {
	fields := map[string]bool{}
	if value.Kind != yaml.MappingNode {
		return fields
	}
	for i := 0; i < len(value.Content); i += 2 {
		fields[value.Content[i].Value] = true
	}
	return fields
}

func removeKeys(values map[string]any, keys ...string) map[string]any {
	for _, key := range keys {
		delete(values, key)
	}
	if len(values) == 0 {
		return nil
	}
	return values
}
