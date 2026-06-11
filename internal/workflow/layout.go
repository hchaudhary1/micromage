package workflow

import (
	"sort"
	"strconv"
)

const (
	NodeWidth  = 190
	NodeHeight = 82
	ColumnGap  = 72
	RowGap     = 88
)

type EdgeView struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Target string `json:"target"`
}

type NodeView struct {
	ID        string            `json:"id"`
	Type      string            `json:"type"`
	Label     string            `json:"label"`
	Summary   string            `json:"summary,omitempty"`
	Layer     int               `json:"layer"`
	X         int               `json:"x"`
	Y         int               `json:"y"`
	DependsOn []string          `json:"depends_on,omitempty"`
	Badges    []string          `json:"badges,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Issues    []Issue           `json:"issues,omitempty"`
}

type GraphView struct {
	Nodes  []NodeView `json:"nodes"`
	Edges  []EdgeView `json:"edges"`
	Width  int        `json:"width"`
	Height int        `json:"height"`
}

func BuildGraph(workflow Workflow, issues []Issue) GraphView {
	layers := TopologicalLayers(workflow.Nodes)
	indexByID := map[string]int{}
	for i, node := range workflow.Nodes {
		indexByID[node.ID] = i
	}

	layerBuckets := map[int][]Node{}
	maxLayer := 0
	for _, node := range workflow.Nodes {
		layer := layers[node.ID]
		layerBuckets[layer] = append(layerBuckets[layer], node)
		if layer > maxLayer {
			maxLayer = layer
		}
	}
	for layer := range layerBuckets {
		sort.SliceStable(layerBuckets[layer], func(i, j int) bool {
			return indexByID[layerBuckets[layer][i].ID] < indexByID[layerBuckets[layer][j].ID]
		})
	}

	nodeIssues := issuesByNode(issues)
	var views []NodeView
	maxCount := 1
	for layer := 0; layer <= maxLayer; layer++ {
		bucket := layerBuckets[layer]
		if len(bucket) > maxCount {
			maxCount = len(bucket)
		}
		for row, node := range bucket {
			views = append(views, NodeView{
				ID:        node.ID,
				Type:      node.Kind(),
				Label:     node.Label(),
				Summary:   node.ContentPreview(),
				Layer:     layer,
				X:         32 + layer*(NodeWidth+ColumnGap),
				Y:         32 + row*(NodeHeight+RowGap),
				DependsOn: node.DependsOn,
				Badges:    nodeBadges(node),
				Metadata:  nodeMetadata(node),
				Issues:    nodeIssues[node.ID],
			})
		}
	}

	return GraphView{
		Nodes:  views,
		Edges:  buildEdges(workflow.Nodes),
		Width:  64 + (maxLayer+1)*NodeWidth + maxLayer*ColumnGap,
		Height: 64 + maxCount*NodeHeight + (maxCount-1)*RowGap,
	}
}

func TopologicalLayers(nodes []Node) map[string]int {
	ids := map[string]bool{}
	for _, node := range nodes {
		if node.ID != "" {
			ids[node.ID] = true
		}
	}

	layers := map[string]int{}
	remaining := map[string]Node{}
	for _, node := range nodes {
		if node.ID != "" {
			remaining[node.ID] = node
		}
	}

	for len(remaining) > 0 {
		progress := false
		for _, node := range nodes {
			if _, ok := remaining[node.ID]; !ok {
				continue
			}
			layer := 0
			ready := true
			for _, dep := range node.DependsOn {
				if !ids[dep] {
					continue
				}
				depLayer, ok := layers[dep]
				if !ok {
					ready = false
					break
				}
				if depLayer+1 > layer {
					layer = depLayer + 1
				}
			}
			if ready {
				layers[node.ID] = layer
				delete(remaining, node.ID)
				progress = true
			}
		}
		if !progress {
			for id := range remaining {
				layers[id] = 0
				delete(remaining, id)
			}
		}
	}
	return layers
}

func HasCycle(nodes []Node) bool {
	ids := map[string]bool{}
	inDegree := map[string]int{}
	adjacency := map[string][]string{}
	for _, node := range nodes {
		if node.ID == "" {
			continue
		}
		ids[node.ID] = true
		inDegree[node.ID] = 0
		adjacency[node.ID] = nil
	}
	for _, node := range nodes {
		for _, dep := range node.DependsOn {
			if ids[node.ID] && ids[dep] {
				inDegree[node.ID]++
				adjacency[dep] = append(adjacency[dep], node.ID)
			}
		}
	}
	var queue []string
	for id := range ids {
		if inDegree[id] == 0 {
			queue = append(queue, id)
		}
	}
	visited := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		visited++
		for _, next := range adjacency[id] {
			inDegree[next]--
			if inDegree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	return visited < len(ids)
}

func buildEdges(nodes []Node) []EdgeView {
	ids := map[string]bool{}
	for _, node := range nodes {
		ids[node.ID] = true
	}
	var edges []EdgeView
	for _, node := range nodes {
		for _, dep := range node.DependsOn {
			if ids[dep] {
				edges = append(edges, EdgeView{
					ID:     dep + "->" + node.ID,
					Source: dep,
					Target: node.ID,
				})
			}
		}
	}
	return edges
}

func issuesByNode(issues []Issue) map[string][]Issue {
	out := map[string][]Issue{}
	for _, issue := range issues {
		if issue.NodeID != "" {
			out[issue.NodeID] = append(out[issue.NodeID], issue)
		}
	}
	return out
}

func nodeBadges(node Node) []string {
	var badges []string
	if node.Model != "" {
		badges = append(badges, node.Model)
	}
	if node.Provider != "" {
		badges = append(badges, node.Provider)
	}
	if node.Context != "" {
		badges = append(badges, node.Context)
	}
	if node.Agent != "" {
		badges = append(badges, node.Agent)
	}
	if node.When != "" {
		badges = append(badges, "when")
	}
	if node.TriggerRule != "" {
		badges = append(badges, node.TriggerRule)
	}
	if node.Retry != nil {
		badges = append(badges, "retry")
	}
	if node.MCP != "" {
		badges = append(badges, "mcp")
	}
	if len(node.Skills) > 0 {
		badges = append(badges, "skills")
	}
	if node.Hooks != nil {
		badges = append(badges, "hooks")
	}
	return badges
}

func nodeMetadata(node Node) map[string]string {
	metadata := map[string]string{
		"id":   node.ID,
		"type": node.Kind(),
	}
	if len(node.DependsOn) > 0 {
		metadata["depends_on"] = stringsJoin(node.DependsOn)
	}
	if node.Provider != "" {
		metadata["provider"] = node.Provider
	}
	if node.Model != "" {
		metadata["model"] = node.Model
	}
	if node.Context != "" {
		metadata["context"] = node.Context
	}
	if node.Agent != "" {
		metadata["agent"] = node.Agent
	}
	if node.IdleTimeout != nil {
		metadata["idle_timeout"] = strconv.Itoa(*node.IdleTimeout)
	}
	if len(node.AllowedTools) > 0 {
		metadata["allowed_tools"] = stringsJoin(node.AllowedTools)
	}
	if node.When != "" {
		metadata["when"] = node.When
	}
	if node.TriggerRule != "" {
		metadata["trigger_rule"] = node.TriggerRule
	}
	if node.MCP != "" {
		metadata["mcp"] = node.MCP
	}
	if len(node.Skills) > 0 {
		metadata["skills"] = stringsJoin(node.Skills)
	}
	if node.ContentPreview() != "" {
		metadata["preview"] = node.ContentPreview()
	}
	return metadata
}

func stringsJoin(values []string) string {
	out := ""
	for i, value := range values {
		if i > 0 {
			out += ", "
		}
		out += value
	}
	return out
}
