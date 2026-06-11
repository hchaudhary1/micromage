package workflow

import (
	"context"
	"sort"
	"sync"
)

type RunEvent struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	NodeID  string `json:"node_id,omitempty"`
	Layer   int    `json:"layer,omitempty"`
}

type EventSink func(RunEvent) error

type NodeRunner interface {
	RunNode(context.Context, Node, EventSink) error
}

type LoggingRunner struct{}

func (LoggingRunner) RunNode(ctx context.Context, node Node, emit EventSink) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	// Fake execution keeps the UI contract real before runtime integrations exist.
	return emit(RunEvent{Type: "node_log", NodeID: node.ID, Message: "would run " + node.Kind() + " node " + node.ID})
}

func Execute(ctx context.Context, workflow Workflow, runner NodeRunner, emit EventSink) error {
	var emitMu sync.Mutex
	emitSafe := func(event RunEvent) error {
		emitMu.Lock()
		defer emitMu.Unlock()
		return emit(event)
	}

	if err := emitSafe(RunEvent{Type: "workflow_start", Message: "starting " + workflow.Name}); err != nil {
		return err
	}

	for _, layer := range scheduleLayers(workflow.Nodes) {
		if len(layer.Nodes) == 0 {
			continue
		}
		if err := emitSafe(RunEvent{Type: "layer_start", Layer: layer.Index, Message: layerMessage("starting layer", layer.Nodes)}); err != nil {
			return err
		}

		var wg sync.WaitGroup
		errs := make(chan error, len(layer.Nodes)*3)
		for _, node := range layer.Nodes {
			node := node
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs <- emitSafe(RunEvent{Type: "node_start", NodeID: node.ID, Layer: layer.Index, Message: "starting " + node.ID})
				if err := runner.RunNode(ctx, node, emitSafe); err != nil {
					errs <- err
					return
				}
				errs <- emitSafe(RunEvent{Type: "node_complete", NodeID: node.ID, Layer: layer.Index, Message: "completed " + node.ID})
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				return err
			}
		}
		if err := emitSafe(RunEvent{Type: "layer_complete", Layer: layer.Index, Message: layerMessage("completed layer", layer.Nodes)}); err != nil {
			return err
		}
	}
	return emitSafe(RunEvent{Type: "workflow_complete", Message: "completed " + workflow.Name})
}

type ScheduledLayer struct {
	Index int
	Nodes []Node
}

func scheduleLayers(nodes []Node) []ScheduledLayer {
	layerByID := TopologicalLayers(nodes)
	buckets := map[int][]Node{}
	var maxLayer int
	for _, node := range nodes {
		layer := layerByID[node.ID]
		buckets[layer] = append(buckets[layer], node)
		if layer > maxLayer {
			maxLayer = layer
		}
	}

	layers := make([]ScheduledLayer, 0, maxLayer+1)
	for i := 0; i <= maxLayer; i++ {
		layerNodes := append([]Node(nil), buckets[i]...)
		sort.SliceStable(layerNodes, func(a, b int) bool {
			return layerNodes[a].ID < layerNodes[b].ID
		})
		layers = append(layers, ScheduledLayer{Index: i, Nodes: layerNodes})
	}
	return layers
}

func layerMessage(prefix string, nodes []Node) string {
	ids := make([]string, 0, len(nodes))
	for _, node := range nodes {
		ids = append(ids, node.ID)
	}
	sort.Strings(ids)
	return prefix + ": " + stringsJoin(ids)
}
