package workflow

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

type RunEvent struct {
	Type           string        `json:"type"`
	Message        string        `json:"message"`
	NodeID         string        `json:"node_id,omitempty"`
	Layer          int           `json:"layer,omitempty"`
	RunID          string        `json:"run_id,omitempty"`
	ArtifactsDir   string        `json:"artifacts_dir,omitempty"`
	Artifacts      []RunArtifact `json:"artifacts,omitempty"`
	CompletedNodes []string      `json:"completed_nodes,omitempty"`
	FailedNodes    []RunFailure  `json:"failed_nodes,omitempty"`
}

type RunArtifact struct {
	NodeID string `json:"node_id"`
	Path   string `json:"path"`
}

type RunFailure struct {
	NodeID  string `json:"node_id"`
	Message string `json:"message"`
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

	statuses := map[string]string{}
	var firstErr error
	var workflowErr error
	failed := map[string]bool{}
	failureReasons := map[string]string{}
	completedAfterFailure := map[string]bool{}

	for _, layer := range scheduleLayers(workflow.Nodes) {
		if len(layer.Nodes) == 0 {
			continue
		}
		if err := emitSafe(RunEvent{Type: "layer_start", Layer: layer.Index, Message: layerMessage("starting layer", layer.Nodes)}); err != nil {
			return err
		}

		type runResult struct {
			nodeID string
			err    error
		}
		var wg sync.WaitGroup
		results := make(chan runResult, len(layer.Nodes))
		for _, node := range layer.Nodes {
			node := node
			if !shouldRunNode(node, statuses) {
				statuses[node.ID] = "skipped"
				message := "skipped " + node.ID
				if err := oneSuccessSkipError(node, statuses, failureReasons); err != nil {
					message = err.Error()
					if workflowErr == nil {
						workflowErr = err
					}
				}
				if err := emitSafe(RunEvent{Type: "node_skipped", NodeID: node.ID, Layer: layer.Index, Message: message}); err != nil {
					return err
				}
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := emitSafe(RunEvent{Type: "node_start", NodeID: node.ID, Layer: layer.Index, Message: "starting " + node.ID}); err != nil {
					results <- runResult{nodeID: node.ID, err: err}
					return
				}
				if err := runner.RunNode(ctx, node, emitSafe); err != nil {
					if emitErr := emitSafe(RunEvent{Type: "node_failed", NodeID: node.ID, Layer: layer.Index, Message: err.Error()}); emitErr != nil {
						results <- runResult{nodeID: node.ID, err: emitErr}
						return
					}
					results <- runResult{nodeID: node.ID, err: err}
					return
				}
				if err := emitSafe(RunEvent{Type: "node_complete", NodeID: node.ID, Layer: layer.Index, Message: "completed " + node.ID}); err != nil {
					results <- runResult{nodeID: node.ID, err: err}
					return
				}
				results <- runResult{nodeID: node.ID}
			}()
		}
		wg.Wait()
		close(results)
		for result := range results {
			if result.err != nil {
				if firstErr == nil {
					firstErr = result.err
				}
				statuses[result.nodeID] = "failed"
				failed[result.nodeID] = true
				failureReasons[result.nodeID] = result.err.Error()
				continue
			}
			statuses[result.nodeID] = "success"
		}
		for _, node := range layer.Nodes {
			if statuses[node.ID] == "success" {
				for _, dep := range node.DependsOn {
					if failed[dep] {
						completedAfterFailure[dep] = true
					}
				}
			}
		}
		if err := emitSafe(RunEvent{Type: "layer_complete", Layer: layer.Index, Message: layerMessage("completed layer", layer.Nodes)}); err != nil {
			return err
		}
	}
	if workflowErr != nil {
		return workflowErr
	}
	if firstErr != nil && hasUntoleratedFailure(failed, completedAfterFailure) {
		return firstErr
	}
	return emitSafe(RunEvent{Type: "workflow_complete", Message: "completed " + workflow.Name})
}

func shouldRunNode(node Node, statuses map[string]string) bool {
	if len(node.DependsOn) == 0 {
		return true
	}
	successes := 0
	failures := 0
	done := 0
	for _, dep := range node.DependsOn {
		switch statuses[dep] {
		case "success":
			successes++
			done++
		case "failed":
			failures++
			done++
		case "skipped":
			done++
		}
	}
	rule := node.TriggerRule
	if rule == "" {
		rule = "all_success"
	}
	switch rule {
	case "one_success":
		return successes > 0
	case "none_failed_min_one_success":
		return failures == 0 && successes > 0
	case "all_done":
		return done == len(node.DependsOn)
	default:
		return successes == len(node.DependsOn)
	}
}

func hasUntoleratedFailure(failed map[string]bool, completedAfterFailure map[string]bool) bool {
	for id := range failed {
		if !completedAfterFailure[id] {
			return true
		}
	}
	return false
}

func oneSuccessSkipError(node Node, statuses map[string]string, failureReasons map[string]string) error {
	if node.TriggerRule != "one_success" || len(node.DependsOn) == 0 {
		return nil
	}
	for _, dep := range node.DependsOn {
		if statuses[dep] == "success" {
			return nil
		}
	}
	failedDeps := make([]string, 0, len(node.DependsOn))
	for _, dep := range node.DependsOn {
		if statuses[dep] == "failed" {
			reason := failureReasons[dep]
			if reason == "" {
				reason = "failed without a recorded reason"
			}
			failedDeps = append(failedDeps, dep+": "+reason)
		}
	}
	// Review synthesis must fail loudly when every reviewer failed instead of producing an empty result.
	if len(failedDeps) > 0 {
		return fmt.Errorf("skipped %s because trigger_rule one_success had no successful dependencies; failed dependencies: %s", node.ID, stringsJoin(failedDeps))
	}
	return fmt.Errorf("skipped %s because trigger_rule one_success had no successful dependencies", node.ID)
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
