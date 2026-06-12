package workflow

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type NodeType string

const (
	NodeCommand   NodeType = "command"
	NodeHumanGate NodeType = "human_gate"
	NodeAgent     NodeType = "agent"
	NodeLoop      NodeType = "loop"
)

type Workflow struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description,omitempty"`
	Provider    string         `yaml:"provider,omitempty"`
	Model       string         `yaml:"model,omitempty"`
	Interactive bool           `yaml:"interactive,omitempty"`
	Worktree    map[string]any `yaml:"worktree,omitempty"`
	Nodes       map[string]Node
}

type Node struct {
	ID           string        `yaml:"id,omitempty"`
	Type         NodeType      `yaml:"type,omitempty"`
	Command      string        `yaml:"command,omitempty"`
	Args         []string      `yaml:"args,omitempty"`
	Bash         string        `yaml:"bash,omitempty"`
	Prompt       string        `yaml:"prompt,omitempty"`
	Approval     *Approval     `yaml:"approval,omitempty"`
	Loop         *Loop         `yaml:"loop,omitempty"`
	Route        *Route        `yaml:"route,omitempty"`
	Provider     string        `yaml:"provider,omitempty"`
	Model        string        `yaml:"model,omitempty"`
	Context      string        `yaml:"context,omitempty"`
	DependsOn    []string      `yaml:"depends_on,omitempty"`
	Timeout      time.Duration `yaml:"-"`
	RawTimeout   string        `yaml:"timeout,omitempty"`
	IdleTimeout  string        `yaml:"idle_timeout,omitempty"`
	TriggerRule  string        `yaml:"trigger_rule,omitempty"`
	When         string        `yaml:"when,omitempty"`
	AllowedTools []string      `yaml:"allowed_tools,omitempty"`
	DeniedTools  []string      `yaml:"denied_tools,omitempty"`
	Skills       []string      `yaml:"skills,omitempty"`
	Hooks        any           `yaml:"hooks,omitempty"`
	MCP          any           `yaml:"mcp,omitempty"`
	OutputFormat any           `yaml:"output_format,omitempty"`
	Message      string        `yaml:"message,omitempty"`
}

type Approval struct {
	Message         string `yaml:"message,omitempty"`
	CaptureResponse bool   `yaml:"capture_response,omitempty"`
}

type Loop struct {
	Prompt        string `yaml:"prompt,omitempty"`
	Until         string `yaml:"until,omitempty"`
	MaxIterations int    `yaml:"max_iterations,omitempty"`
	FreshContext  bool   `yaml:"fresh_context,omitempty"`
}

type Route struct {
	OnFailure *RouteTarget `yaml:"on_failure,omitempty"`
}

type RouteTarget struct {
	To                  string `yaml:"to,omitempty"`
	MaxIterations       int    `yaml:"max_iterations,omitempty"`
	MaxRepeatedFailures int    `yaml:"max_repeated_failures,omitempty"`
}

func Parse(r io.Reader) (*Workflow, error) {
	dec := yaml.NewDecoder(r)

	var wf Workflow
	if err := dec.Decode(&wf); err != nil {
		return nil, fmt.Errorf("parse workflow: %w", err)
	}
	if err := wf.Validate(); err != nil {
		return nil, err
	}
	return &wf, nil
}

func (wf *Workflow) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("workflow must be a mapping")
	}
	allowed := map[string]bool{
		"name": true, "description": true, "provider": true, "model": true,
		"interactive": true, "worktree": true, "nodes": true,
	}
	var nodesNode *yaml.Node
	for i := 0; i < len(value.Content); i += 2 {
		key := value.Content[i].Value
		if !allowed[key] {
			return fmt.Errorf("workflow has unknown field %q", key)
		}
		val := value.Content[i+1]
		switch key {
		case "name":
			wf.Name = val.Value
		case "description":
			wf.Description = val.Value
		case "provider":
			wf.Provider = val.Value
		case "model":
			wf.Model = val.Value
		case "interactive":
			if err := val.Decode(&wf.Interactive); err != nil {
				return err
			}
		case "worktree":
			if err := val.Decode(&wf.Worktree); err != nil {
				return err
			}
		case "nodes":
			nodesNode = val
		}
	}
	if nodesNode == nil {
		return nil
	}
	nodes, err := decodeNodes(nodesNode)
	if err != nil {
		return err
	}
	wf.Nodes = nodes
	return nil
}

func (n *Node) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("node must be a mapping")
	}
	allowed := map[string]bool{
		"id": true, "type": true, "command": true, "args": true, "bash": true,
		"prompt": true, "approval": true, "loop": true, "route": true, "provider": true,
		"model": true, "context": true, "depends_on": true, "timeout": true,
		"idle_timeout": true, "trigger_rule": true, "when": true,
		"allowed_tools": true, "denied_tools": true, "skills": true,
		"hooks": true, "mcp": true, "output_format": true, "message": true,
	}
	type nodeAlias Node
	var out nodeAlias
	for i := 0; i < len(value.Content); i += 2 {
		key := value.Content[i].Value
		if !allowed[key] {
			return fmt.Errorf("node has unknown field %q", key)
		}
	}
	if err := value.Decode(&out); err != nil {
		return err
	}
	*n = Node(out)
	n.deriveType()
	return nil
}

func decodeNodes(value *yaml.Node) (map[string]Node, error) {
	nodes := map[string]Node{}
	switch value.Kind {
	case yaml.MappingNode:
		for i := 0; i < len(value.Content); i += 2 {
			id := value.Content[i].Value
			var node Node
			if err := value.Content[i+1].Decode(&node); err != nil {
				return nil, err
			}
			if node.ID == "" {
				node.ID = id
			}
			nodes[id] = node
		}
	case yaml.SequenceNode:
		for _, item := range value.Content {
			var node Node
			if err := item.Decode(&node); err != nil {
				return nil, err
			}
			if strings.TrimSpace(node.ID) == "" {
				return nil, fmt.Errorf("sequence node is missing id")
			}
			if _, exists := nodes[node.ID]; exists {
				return nil, fmt.Errorf("duplicate node id %q", node.ID)
			}
			nodes[node.ID] = node
		}
	default:
		return nil, fmt.Errorf("nodes must be a mapping or sequence")
	}
	return nodes, nil
}

func (n *Node) deriveType() {
	if n.Type != "" {
		return
	}
	switch {
	case n.Approval != nil:
		n.Type = NodeHumanGate
		n.Message = n.Approval.Message
	case n.Loop != nil:
		n.Type = NodeLoop
		n.Prompt = n.Loop.Prompt
	case strings.TrimSpace(n.Bash) != "":
		n.Type = NodeCommand
		n.Command = n.Bash
	case strings.TrimSpace(n.Prompt) != "", strings.TrimSpace(n.Command) != "":
		n.Type = NodeAgent
	}
}

func (wf *Workflow) Validate() error {
	if strings.TrimSpace(wf.Name) == "" {
		return fmt.Errorf("workflow name is required")
	}
	if len(wf.Nodes) == 0 {
		return fmt.Errorf("workflow must define at least one node")
	}

	for id, node := range wf.Nodes {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("node id cannot be empty")
		}
		if node.Type == "" {
			return fmt.Errorf("node %q type is required", id)
		}
		switch node.Type {
		case NodeCommand:
			if strings.TrimSpace(node.Command) == "" {
				return fmt.Errorf("node %q command is required", id)
			}
		case NodeHumanGate:
			// Human gates preserve reviewer control before risky work proceeds.
		case NodeAgent:
			if strings.TrimSpace(node.Prompt) == "" && strings.TrimSpace(node.Command) == "" {
				return fmt.Errorf("node %q prompt or command template is required", id)
			}
		case NodeLoop:
			if node.Loop == nil || strings.TrimSpace(node.Loop.Prompt) == "" {
				return fmt.Errorf("node %q loop prompt is required", id)
			}
		default:
			return fmt.Errorf("node %q has unsupported type %q", id, node.Type)
		}
		for _, dep := range node.DependsOn {
			if _, ok := wf.Nodes[dep]; !ok {
				return fmt.Errorf("node %q has unknown dependency %q", id, dep)
			}
		}
		if node.Route != nil && node.Route.OnFailure != nil {
			target := node.Route.OnFailure
			if strings.TrimSpace(target.To) == "" {
				return fmt.Errorf("node %q failure route target is required", id)
			}
			if _, ok := wf.Nodes[target.To]; !ok {
				return fmt.Errorf("node %q has unknown failure route target %q", id, target.To)
			}
			if target.MaxIterations < 0 {
				return fmt.Errorf("node %q failure route max_iterations cannot be negative", id)
			}
			if target.MaxRepeatedFailures < 0 {
				return fmt.Errorf("node %q failure route max_repeated_failures cannot be negative", id)
			}
		}
		if node.RawTimeout != "" {
			timeout, err := parseTimeout(node.RawTimeout)
			if err != nil {
				return fmt.Errorf("node %q timeout: %w", id, err)
			}
			node.Timeout = timeout
			wf.Nodes[id] = node
		}
	}
	if _, err := wf.PlanLayers(); err != nil {
		return err
	}
	return nil
}

func (wf *Workflow) HasRoutes() bool {
	for _, node := range wf.Nodes {
		if node.Route != nil && node.Route.OnFailure != nil && strings.TrimSpace(node.Route.OnFailure.To) != "" {
			return true
		}
	}
	return false
}

func parseTimeout(raw string) (time.Duration, error) {
	if ms, err := strconv.Atoi(raw); err == nil {
		// Numeric compatibility keeps migrated workflow timeouts in milliseconds.
		return time.Duration(ms) * time.Millisecond, nil
	}
	return time.ParseDuration(raw)
}

func (wf *Workflow) PlanLayers() ([][]string, error) {
	indegree := map[string]int{}
	children := map[string][]string{}
	for id, node := range wf.Nodes {
		indegree[id] = len(node.DependsOn)
		for _, dep := range node.DependsOn {
			children[dep] = append(children[dep], id)
		}
	}

	var layers [][]string
	ready := readyIDs(indegree)
	visited := 0
	for len(ready) > 0 {
		layer := append([]string(nil), ready...)
		layers = append(layers, layer)
		visited += len(layer)
		for _, id := range layer {
			for _, child := range children[id] {
				indegree[child]--
			}
			delete(indegree, id)
		}
		ready = readyIDs(indegree)
	}
	if visited != len(wf.Nodes) {
		return nil, fmt.Errorf("cycle detected in workflow dependencies")
	}
	return layers, nil
}

func readyIDs(indegree map[string]int) []string {
	var ready []string
	for id, degree := range indegree {
		if degree == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)
	return ready
}
