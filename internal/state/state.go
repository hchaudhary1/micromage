package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type NodeStatus string

const (
	StatusPassed  NodeStatus = "passed"
	StatusFailed  NodeStatus = "failed"
	StatusSkipped NodeStatus = "skipped"
	StatusPaused  NodeStatus = "paused"
)

type RunState struct {
	RunID        string               `json:"run_id"`
	Workflow     string               `json:"workflow"`
	WorkflowPath string               `json:"workflow_path,omitempty"`
	PausedNode   string               `json:"paused_node,omitempty"`
	Nodes        map[string]NodeState `json:"nodes"`
	UpdatedAt    time.Time            `json:"updated_at"`
}

type NodeState struct {
	Status     NodeStatus `json:"status"`
	Output     string     `json:"output,omitempty"`
	Message    string     `json:"message,omitempty"`
	ApprovedAt time.Time  `json:"approved_at,omitempty"`
	ApprovedBy string     `json:"approved_by,omitempty"`
	Comment    string     `json:"comment,omitempty"`
}

func NewRun(runID, workflow, workflowPath string) *RunState {
	return &RunState{
		RunID:        runID,
		Workflow:     workflow,
		WorkflowPath: workflowPath,
		Nodes:        map[string]NodeState{},
	}
}

func Load(path string) (*RunState, error) {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rs RunState
	if err := json.Unmarshal(bytes, &rs); err != nil {
		return nil, fmt.Errorf("parse run state: %w", err)
	}
	if rs.Nodes == nil {
		rs.Nodes = map[string]NodeState{}
	}
	return &rs, nil
}

func Save(path string, rs *RunState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	rs.UpdatedAt = time.Now().UTC()
	bytes, err := json.MarshalIndent(rs, "", "  ")
	if err != nil {
		return err
	}
	bytes = append(bytes, '\n')
	// Atomic replacement keeps reviewers from approving partially written state.
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(bytes); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

func (rs *RunState) MarkPassed(nodeID, output string) {
	rs.ensureNodes()
	rs.Nodes[nodeID] = NodeState{Status: StatusPassed, Output: output}
	if rs.PausedNode == nodeID {
		rs.PausedNode = ""
	}
}

func (rs *RunState) MarkFailed(nodeID, output, message string) {
	rs.ensureNodes()
	rs.Nodes[nodeID] = NodeState{Status: StatusFailed, Output: output, Message: message}
}

func (rs *RunState) MarkSkipped(nodeID, message string) {
	rs.ensureNodes()
	rs.Nodes[nodeID] = NodeState{Status: StatusSkipped, Message: message}
}

func (rs *RunState) MarkPaused(nodeID, message string) {
	rs.ensureNodes()
	rs.PausedNode = nodeID
	rs.Nodes[nodeID] = NodeState{Status: StatusPaused, Message: message}
}

func (rs *RunState) Approve(nodeID, reviewer, comment string, at time.Time) error {
	rs.ensureNodes()
	if rs.PausedNode == "" {
		return errors.New("run is not paused")
	}
	if rs.PausedNode != nodeID {
		return fmt.Errorf("run is paused at %q, not %q", rs.PausedNode, nodeID)
	}
	node, ok := rs.Nodes[nodeID]
	if !ok || node.Status != StatusPaused {
		return fmt.Errorf("node %q is not paused", nodeID)
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	// Approved gates become completed dependencies so resume starts after review.
	node.Status = StatusPassed
	node.ApprovedAt = at.UTC()
	node.ApprovedBy = reviewer
	node.Comment = comment
	rs.Nodes[nodeID] = node
	rs.PausedNode = ""
	return nil
}

func (rs *RunState) ensureNodes() {
	if rs.Nodes == nil {
		rs.Nodes = map[string]NodeState{}
	}
}
