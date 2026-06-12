package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hchaudhary1/micromage/internal/runlog"
	"github.com/hchaudhary1/micromage/internal/workflow"
)

var ErrHumanGate = errors.New("workflow paused at human gate")

const DefaultNodeTimeout = 10 * time.Minute

type Runner interface {
	Run(ctx context.Context, nodeID string, node workflow.Node, record func(string)) error
}

type Engine struct {
	runner   Runner
	recorder *runlog.Recorder
}

type RunOptions struct {
	InitialResults map[string]NodeSnapshot
	OnNodeResult   func(NodeSnapshot)
}

type NodeSnapshot struct {
	ID      string
	Status  string
	Output  string
	Message string
}

type nodeStatus string

const (
	statusPassed  nodeStatus = "passed"
	statusFailed  nodeStatus = "failed"
	statusSkipped nodeStatus = "skipped"
)

type nodeResult struct {
	status nodeStatus
	output string
	err    error
}

type runOutcome struct {
	output string
	err    error
}

func New(runner Runner, recorder *runlog.Recorder) *Engine {
	return &Engine{runner: runner, recorder: recorder}
}

func (e *Engine) Run(ctx context.Context, wf *workflow.Workflow) error {
	return e.RunWithOptions(ctx, wf, RunOptions{})
}

func (e *Engine) RunWithOptions(ctx context.Context, wf *workflow.Workflow, opts RunOptions) error {
	layers, err := wf.PlanLayers()
	if err != nil {
		return err
	}
	e.record(runlog.Event{Type: runlog.EventWorkflowStarted, Message: wf.Name})
	if wf.HasRoutes() {
		return e.runWithRoutes(ctx, wf, layers, opts)
	}

	results := initialNodeResults(opts.InitialResults)
	var firstErr error
	for _, layer := range layers {
		layerErr := e.runLayer(ctx, wf, layer, results, opts.OnNodeResult)
		if layerErr != nil && firstErr == nil {
			firstErr = layerErr
		}
		if errors.Is(layerErr, ErrHumanGate) {
			break
		}
	}
	if firstErr != nil {
		e.record(runlog.Event{Type: runlog.EventWorkflowFailed, Message: firstErr.Error()})
		return firstErr
	}
	e.record(runlog.Event{Type: runlog.EventWorkflowPassed, Message: wf.Name})
	return nil
}

func (e *Engine) runLayer(ctx context.Context, wf *workflow.Workflow, layer []string, results map[string]nodeResult, onResult func(NodeSnapshot)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	errs := make(chan error, len(layer))
	var resultMu sync.Mutex
	for _, id := range layer {
		id := id
		node := wf.Nodes[id]
		if result, ok := results[id]; ok && result.status == statusPassed {
			// Resume mode trusts persisted successful work instead of spending tokens twice.
			e.record(runlog.Event{Type: runlog.EventNodeSkipped, NodeID: id, Message: "already completed"})
			emitNodeResult(onResult, id, result.status, result.output, "already completed")
			continue
		}
		if !dependenciesReady(node, results) {
			results[id] = nodeResult{status: statusSkipped}
			e.record(runlog.Event{Type: runlog.EventNodeSkipped, NodeID: id, Message: "dependency trigger did not match"})
			emitNodeResult(onResult, id, statusSkipped, "", "dependency trigger did not match")
			continue
		}
		if !whenMatches(node.When, results) {
			results[id] = nodeResult{status: statusSkipped}
			e.record(runlog.Event{Type: runlog.EventNodeSkipped, NodeID: id, Message: "condition evaluated false"})
			emitNodeResult(onResult, id, statusSkipped, "", "condition evaluated false")
			continue
		}
		if node.Type == workflow.NodeHumanGate {
			// Review gates stop automation before irreversible work continues.
			e.record(runlog.Event{Type: runlog.EventNodePaused, NodeID: id, Message: node.Message})
			results[id] = nodeResult{status: nodeStatus("paused")}
			emitNodeResult(onResult, id, "paused", "", node.Message)
			return fmt.Errorf("%w: %s", ErrHumanGate, id)
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			outcome := e.executeNode(ctx, id, node)
			if outcome.err != nil {
				cancel()
				resultMu.Lock()
				results[id] = nodeResult{status: statusFailed, output: outcome.output, err: outcome.err}
				resultMu.Unlock()
				emitNodeResult(onResult, id, statusFailed, outcome.output, outcome.err.Error())
				errs <- fmt.Errorf("node %s failed: %w", id, outcome.err)
				return
			}
			resultMu.Lock()
			results[id] = nodeResult{status: statusPassed, output: outcome.output}
			resultMu.Unlock()
			emitNodeResult(onResult, id, statusPassed, outcome.output, "")
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) runWithRoutes(ctx context.Context, wf *workflow.Workflow, layers [][]string, opts RunOptions) error {
	order := flattenLayers(layers)
	positions := nodePositions(order)
	if err := validateRoutePositions(wf, positions); err != nil {
		e.record(runlog.Event{Type: runlog.EventWorkflowFailed, Message: err.Error()})
		return err
	}

	results := initialNodeResults(opts.InitialResults)
	routeAttempts := map[string]int{}
	repeatedFailures := map[string]map[string]int{}
	for i := 0; i < len(order); {
		id := order[i]
		node := wf.Nodes[id]
		if result, ok := results[id]; ok && result.status == statusPassed {
			// Resume mode trusts persisted successful work until a repair route invalidates it.
			e.record(runlog.Event{Type: runlog.EventNodeSkipped, NodeID: id, Message: "already completed"})
			emitNodeResult(opts.OnNodeResult, id, result.status, result.output, "already completed")
			i++
			continue
		}
		if !dependenciesReady(node, results) {
			results[id] = nodeResult{status: statusSkipped}
			e.record(runlog.Event{Type: runlog.EventNodeSkipped, NodeID: id, Message: "dependency trigger did not match"})
			emitNodeResult(opts.OnNodeResult, id, statusSkipped, "", "dependency trigger did not match")
			i++
			continue
		}
		if !whenMatches(node.When, results) {
			results[id] = nodeResult{status: statusSkipped}
			e.record(runlog.Event{Type: runlog.EventNodeSkipped, NodeID: id, Message: "condition evaluated false"})
			emitNodeResult(opts.OnNodeResult, id, statusSkipped, "", "condition evaluated false")
			i++
			continue
		}
		if node.Type == workflow.NodeHumanGate {
			// Review gates stop automation before irreversible work continues.
			e.record(runlog.Event{Type: runlog.EventNodePaused, NodeID: id, Message: node.Message})
			results[id] = nodeResult{status: nodeStatus("paused")}
			emitNodeResult(opts.OnNodeResult, id, "paused", "", node.Message)
			err := fmt.Errorf("%w: %s", ErrHumanGate, id)
			e.record(runlog.Event{Type: runlog.EventWorkflowFailed, Message: err.Error()})
			return err
		}

		outcome := e.executeNode(ctx, id, node)
		if outcome.err == nil {
			results[id] = nodeResult{status: statusPassed, output: outcome.output}
			emitNodeResult(opts.OnNodeResult, id, statusPassed, outcome.output, "")
			i++
			continue
		}
		results[id] = nodeResult{status: statusFailed, output: outcome.output, err: outcome.err}
		emitNodeResult(opts.OnNodeResult, id, statusFailed, outcome.output, outcome.err.Error())
		route := failureRoute(node)
		if route == nil {
			err := fmt.Errorf("node %s failed: %w", id, outcome.err)
			e.record(runlog.Event{Type: runlog.EventWorkflowFailed, Message: err.Error()})
			return err
		}
		if route.MaxIterations > 0 && routeAttempts[id] >= route.MaxIterations {
			// Bounded repair routes keep cyclic workflows from consuming the operator's session forever.
			err := fmt.Errorf("node %s failure route to %s exceeded max_iterations %d: %w", id, route.To, route.MaxIterations, outcome.err)
			e.record(runlog.Event{Type: runlog.EventWorkflowFailed, Message: err.Error()})
			return err
		}
		fingerprint := failureFingerprint(outcome)
		if repeatedFailures[id] == nil {
			repeatedFailures[id] = map[string]int{}
		}
		repeatedFailures[id][fingerprint]++
		if route.MaxRepeatedFailures > 0 && repeatedFailures[id][fingerprint] >= route.MaxRepeatedFailures {
			// Repeated-failure guards stop repair loops that are making no meaningful progress.
			err := fmt.Errorf("node %s failure route to %s saw repeated failure %d times: %w", id, route.To, repeatedFailures[id][fingerprint], outcome.err)
			e.record(runlog.Event{Type: runlog.EventWorkflowFailed, Message: err.Error()})
			return err
		}
		routeAttempts[id]++
		targetPos := positions[route.To]
		e.record(runlog.Event{Type: runlog.EventNodeOutput, NodeID: id, Message: fmt.Sprintf("routing failure to %s (%d/%d)", route.To, routeAttempts[id], route.MaxIterations)})
		clearRerunResults(results, order[targetPos:i+1])
		i = targetPos
	}
	e.record(runlog.Event{Type: runlog.EventWorkflowPassed, Message: wf.Name})
	return nil
}

func (e *Engine) executeNode(ctx context.Context, id string, node workflow.Node) runOutcome {
	var output strings.Builder
	var outputMu sync.Mutex
	e.record(runlog.Event{Type: runlog.EventNodeStarted, NodeID: id})
	runCtx := ctx
	timeout := node.Timeout
	if timeout == 0 {
		// Default deadlines prevent interactive CLIs from freezing the run indefinitely.
		timeout = DefaultNodeTimeout
	}
	var nodeCancel context.CancelFunc
	runCtx, nodeCancel = context.WithTimeout(ctx, timeout)
	defer nodeCancel()
	err := e.runner.Run(runCtx, id, node, func(line string) {
		outputMu.Lock()
		output.WriteString(line)
		output.WriteByte('\n')
		outputMu.Unlock()
		e.record(runlog.Event{Type: runlog.EventNodeOutput, NodeID: id, Message: line})
	})
	if err != nil {
		e.record(runlog.Event{Type: runlog.EventNodeFailed, NodeID: id, Message: err.Error()})
		return runOutcome{output: output.String(), err: err}
	}
	e.record(runlog.Event{Type: runlog.EventNodePassed, NodeID: id})
	return runOutcome{output: output.String()}
}

func initialNodeResults(snapshots map[string]NodeSnapshot) map[string]nodeResult {
	results := map[string]nodeResult{}
	for id, snap := range snapshots {
		switch snap.Status {
		case string(statusPassed):
			results[id] = nodeResult{status: statusPassed, output: snap.Output}
		case string(statusSkipped):
			results[id] = nodeResult{status: statusSkipped, output: snap.Output}
		case string(statusFailed):
			results[id] = nodeResult{status: statusFailed, output: snap.Output, err: errors.New(snap.Message)}
		}
	}
	return results
}

func emitNodeResult(onResult func(NodeSnapshot), id string, status nodeStatus, output, message string) {
	if onResult != nil {
		onResult(NodeSnapshot{ID: id, Status: string(status), Output: output, Message: message})
	}
}

func dependenciesReady(node workflow.Node, results map[string]nodeResult) bool {
	if len(node.DependsOn) == 0 {
		return true
	}
	successes := 0
	for _, dep := range node.DependsOn {
		result := results[dep]
		if result.status == statusPassed {
			successes++
		}
	}
	switch node.TriggerRule {
	case "all_done":
		return true
	case "one_success":
		return successes > 0
	default:
		return successes == len(node.DependsOn)
	}
}

func whenMatches(expr string, results map[string]nodeResult) bool {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return true
	}
	op := "=="
	parts := strings.Split(expr, "==")
	if len(parts) != 2 {
		op = "!="
		parts = strings.Split(expr, "!=")
	}
	if len(parts) != 2 {
		return false
	}
	left := resolveValue(strings.TrimSpace(parts[0]), results)
	right := strings.Trim(strings.TrimSpace(parts[1]), "'\"")
	if op == "!=" {
		return left != right
	}
	return left == right
}

func resolveValue(ref string, results map[string]nodeResult) string {
	if !strings.HasPrefix(ref, "$") {
		return strings.Trim(ref, "'\"")
	}
	ref = strings.TrimPrefix(ref, "$")
	parts := strings.Split(ref, ".")
	if len(parts) < 2 || parts[1] != "output" {
		return ""
	}
	result := strings.TrimSpace(results[parts[0]].output)
	if len(parts) == 2 {
		return result
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		return ""
	}
	value, ok := decoded[parts[2]]
	if !ok {
		return ""
	}
	return fmt.Sprint(value)
}

func failureRoute(node workflow.Node) *workflow.RouteTarget {
	if node.Route == nil {
		return nil
	}
	return node.Route.OnFailure
}

func flattenLayers(layers [][]string) []string {
	var order []string
	for _, layer := range layers {
		order = append(order, layer...)
	}
	return order
}

func nodePositions(order []string) map[string]int {
	positions := map[string]int{}
	for i, id := range order {
		positions[id] = i
	}
	return positions
}

func validateRoutePositions(wf *workflow.Workflow, positions map[string]int) error {
	for id, node := range wf.Nodes {
		route := failureRoute(node)
		if route == nil {
			continue
		}
		if positions[route.To] >= positions[id] {
			return fmt.Errorf("node %s failure route target %s must be earlier in dependency order", id, route.To)
		}
	}
	return nil
}

func clearRerunResults(results map[string]nodeResult, ids []string) {
	for _, id := range ids {
		delete(results, id)
	}
}

func failureFingerprint(outcome runOutcome) string {
	message := ""
	if outcome.err != nil {
		message = outcome.err.Error()
	}
	output := strings.TrimSpace(outcome.output)
	if len(output) > 512 {
		output = output[:512]
	}
	return message + "\n" + output
}

func (e *Engine) record(event runlog.Event) {
	if e.recorder != nil {
		e.recorder.Record(event)
	}
}
