package engine

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hchaudhary1/micromage/internal/runlog"
	"github.com/hchaudhary1/micromage/internal/workflow"
)

type fakeRunner struct {
	mu       sync.Mutex
	started  map[string]time.Time
	errs     map[string]error
	duration map[string]time.Duration
	deadline map[string]bool
	maxSeen  int
	running  int
}

func (f *fakeRunner) Run(ctx context.Context, nodeID string, node workflow.Node, record func(string)) error {
	f.mu.Lock()
	if f.started == nil {
		f.started = map[string]time.Time{}
	}
	f.started[nodeID] = time.Now()
	if _, ok := ctx.Deadline(); ok {
		if f.deadline == nil {
			f.deadline = map[string]bool{}
		}
		f.deadline[nodeID] = true
	}
	f.running++
	if f.running > f.maxSeen {
		f.maxSeen = f.running
	}
	delay := f.duration[nodeID]
	err := f.errs[nodeID]
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.running--
		f.mu.Unlock()
	}()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	record("done " + nodeID)
	return err
}

func TestRunExecutesIndependentNodesInParallel(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "parallel",
		Nodes: map[string]workflow.Node{
			"setup": {Type: workflow.NodeCommand, Command: "setup"},
			"a":     {Type: workflow.NodeCommand, Command: "a", DependsOn: []string{"setup"}},
			"b":     {Type: workflow.NodeCommand, Command: "b", DependsOn: []string{"setup"}},
			"done":  {Type: workflow.NodeCommand, Command: "done", DependsOn: []string{"a", "b"}},
		},
	}
	runner := &fakeRunner{duration: map[string]time.Duration{"a": 30 * time.Millisecond, "b": 30 * time.Millisecond}}
	rec := runlog.NewRecorder(nil)

	if err := New(runner, rec).Run(context.Background(), wf); err != nil {
		t.Fatal(err)
	}
	if runner.maxSeen < 2 {
		t.Fatalf("expected parallel execution, max running was %d", runner.maxSeen)
	}
	if !runner.started["done"].After(runner.started["a"]) || !runner.started["done"].After(runner.started["b"]) {
		t.Fatalf("done started before dependencies completed: %#v", runner.started)
	}
}

func TestRunStopsAfterNodeFailure(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "failure",
		Nodes: map[string]workflow.Node{
			"test":  {Type: workflow.NodeCommand, Command: "test"},
			"merge": {Type: workflow.NodeCommand, Command: "merge", DependsOn: []string{"test"}},
		},
	}
	runner := &fakeRunner{errs: map[string]error{"test": errors.New("boom")}}
	var out bytes.Buffer
	rec := runlog.NewRecorder(&out)

	err := New(runner, rec).Run(context.Background(), wf)
	if err == nil || !strings.Contains(err.Error(), "test") {
		t.Fatalf("expected node failure, got %v", err)
	}
	if _, ok := runner.started["merge"]; ok {
		t.Fatal("dependent node ran after failure")
	}
	if !strings.Contains(out.String(), "node_failed") {
		t.Fatalf("expected failure event in log, got %s", out.String())
	}
}

func TestRunContinuesAllDoneAfterFailure(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "all done",
		Nodes: map[string]workflow.Node{
			"review": {Type: workflow.NodeCommand, Command: "review"},
			"report": {Type: workflow.NodeCommand, Command: "report", DependsOn: []string{"review"}, TriggerRule: "all_done"},
		},
	}
	runner := &fakeRunner{errs: map[string]error{"review": errors.New("review failed")}}

	err := New(runner, runlog.NewRecorder(nil)).Run(context.Background(), wf)
	if err == nil {
		t.Fatal("expected workflow to report original failure")
	}
	if _, ok := runner.started["report"]; !ok {
		t.Fatal("expected all_done node to run after failed dependency")
	}
}

func TestRunEvaluatesWhenConditions(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "conditions",
		Nodes: map[string]workflow.Node{
			"classify": {Type: workflow.NodeCommand, Command: "classify"},
			"bug":      {Type: workflow.NodeCommand, Command: "bug", DependsOn: []string{"classify"}, When: "$classify.output.issue_type == 'bug'"},
			"feature":  {Type: workflow.NodeCommand, Command: "feature", DependsOn: []string{"classify"}, When: "$classify.output.issue_type != 'bug'"},
		},
	}
	runner := &fakeRunner{}
	originalRun := runner.Run
	_ = originalRun
	rec := runlog.NewRecorder(nil)
	err := New(runnerFunc(func(ctx context.Context, nodeID string, node workflow.Node, record func(string)) error {
		if nodeID == "classify" {
			record(`{"issue_type":"bug"}`)
			return nil
		}
		return runner.Run(ctx, nodeID, node, record)
	}), rec).Run(context.Background(), wf)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := runner.started["bug"]; !ok {
		t.Fatal("expected bug branch to run")
	}
	if _, ok := runner.started["feature"]; ok {
		t.Fatal("expected feature branch to skip")
	}
}

func TestRunAddsDefaultDeadlineToCommandNodes(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "deadline",
		Nodes: map[string]workflow.Node{
			"test": {Type: workflow.NodeCommand, Command: "test"},
		},
	}
	runner := &fakeRunner{}

	if err := New(runner, runlog.NewRecorder(nil)).Run(context.Background(), wf); err != nil {
		t.Fatal(err)
	}
	if !runner.deadline["test"] {
		t.Fatal("expected default deadline on command node")
	}
}

type runnerFunc func(context.Context, string, workflow.Node, func(string)) error

func (f runnerFunc) Run(ctx context.Context, nodeID string, node workflow.Node, record func(string)) error {
	return f(ctx, nodeID, node, record)
}

func TestRunPausesOnHumanGate(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "gate",
		Nodes: map[string]workflow.Node{
			"review": {Type: workflow.NodeHumanGate, Message: "approve release"},
			"ship":   {Type: workflow.NodeCommand, Command: "ship", DependsOn: []string{"review"}},
		},
	}
	runner := &fakeRunner{}
	rec := runlog.NewRecorder(nil)

	err := New(runner, rec).Run(context.Background(), wf)
	if !errors.Is(err, ErrHumanGate) {
		t.Fatalf("expected human gate pause, got %v", err)
	}
	if _, ok := runner.started["ship"]; ok {
		t.Fatal("ship ran while review gate was paused")
	}
}

func TestRunWithOptionsResumesAfterApprovedHumanGate(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "gate resume",
		Nodes: map[string]workflow.Node{
			"build":  {Type: workflow.NodeCommand, Command: "build"},
			"review": {Type: workflow.NodeHumanGate, Message: "approve release", DependsOn: []string{"build"}},
			"ship":   {Type: workflow.NodeCommand, Command: "ship", DependsOn: []string{"review"}},
		},
	}
	runner := &fakeRunner{}
	var snapshots []NodeSnapshot

	err := New(runner, runlog.NewRecorder(nil)).RunWithOptions(context.Background(), wf, RunOptions{
		InitialResults: map[string]NodeSnapshot{
			"build":  {ID: "build", Status: "passed", Output: "done build\n"},
			"review": {ID: "review", Status: "passed"},
		},
		OnNodeResult: func(snap NodeSnapshot) {
			snapshots = append(snapshots, snap)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := runner.started["build"]; ok {
		t.Fatal("build reran even though state marked it passed")
	}
	if _, ok := runner.started["ship"]; !ok {
		t.Fatal("ship did not run after approved gate")
	}
	if len(snapshots) == 0 {
		t.Fatal("expected resume snapshots to be reported")
	}
}

func TestRunFailureRouteRepairsAndContinues(t *testing.T) {
	wf := retryRouteWorkflow(3, 3)
	counts := map[string]int{}
	err := New(runnerFunc(func(ctx context.Context, nodeID string, node workflow.Node, record func(string)) error {
		counts[nodeID]++
		record("attempt")
		if nodeID == "verify" && counts[nodeID] == 1 {
			return errors.New("tests failed")
		}
		return nil
	}), runlog.NewRecorder(nil)).Run(context.Background(), wf)
	if err != nil {
		t.Fatal(err)
	}
	if counts["repair"] != 2 || counts["verify"] != 2 || counts["deploy"] != 1 {
		t.Fatalf("unexpected route counts: %#v", counts)
	}
}

func TestRunFailureRouteStopsAtMaxIterations(t *testing.T) {
	wf := retryRouteWorkflow(1, 0)
	counts := map[string]int{}
	err := New(runnerFunc(func(ctx context.Context, nodeID string, node workflow.Node, record func(string)) error {
		counts[nodeID]++
		if nodeID == "verify" {
			record("different failure")
			return errors.New("tests failed")
		}
		return nil
	}), runlog.NewRecorder(nil)).Run(context.Background(), wf)
	if err == nil || !strings.Contains(err.Error(), "exceeded max_iterations 1") {
		t.Fatalf("expected max iteration error, got %v", err)
	}
	if counts["repair"] != 2 || counts["verify"] != 2 || counts["deploy"] != 0 {
		t.Fatalf("unexpected max-iteration counts: %#v", counts)
	}
}

func TestRunFailureRouteStopsOnRepeatedFailure(t *testing.T) {
	wf := retryRouteWorkflow(5, 2)
	counts := map[string]int{}
	err := New(runnerFunc(func(ctx context.Context, nodeID string, node workflow.Node, record func(string)) error {
		counts[nodeID]++
		if nodeID == "verify" {
			record("same output")
			return errors.New("same failure")
		}
		return nil
	}), runlog.NewRecorder(nil)).Run(context.Background(), wf)
	if err == nil || !strings.Contains(err.Error(), "saw repeated failure 2 times") {
		t.Fatalf("expected repeated-failure error, got %v", err)
	}
	if counts["repair"] != 2 || counts["verify"] != 2 || counts["deploy"] != 0 {
		t.Fatalf("unexpected repeated-failure counts: %#v", counts)
	}
}

func retryRouteWorkflow(maxIterations, maxRepeatedFailures int) *workflow.Workflow {
	return &workflow.Workflow{
		Name: "repair loop",
		Nodes: map[string]workflow.Node{
			"setup":  {Type: workflow.NodeCommand, Command: "setup"},
			"repair": {Type: workflow.NodeCommand, Command: "repair", DependsOn: []string{"setup"}},
			"verify": {
				Type:      workflow.NodeCommand,
				Command:   "verify",
				DependsOn: []string{"repair"},
				Route: &workflow.Route{OnFailure: &workflow.RouteTarget{
					To:                  "repair",
					MaxIterations:       maxIterations,
					MaxRepeatedFailures: maxRepeatedFailures,
				}},
			},
			"deploy": {Type: workflow.NodeCommand, Command: "deploy", DependsOn: []string{"verify"}},
		},
	}
}
