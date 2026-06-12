package watch

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/hchaudhary1/micromage/internal/runlog"
)

type Status string

const (
	StatusPending Status = "pending"
	StatusRunning Status = "running"
	StatusPassed  Status = "passed"
	StatusFailed  Status = "failed"
	StatusSkipped Status = "skipped"
	StatusPaused  Status = "paused"
)

type NodeView struct {
	ID      string
	Status  Status
	Message string
	Output  []string
}

type Model struct {
	WorkflowName   string
	WorkflowStatus Status
	Nodes          map[string]*NodeView
	Recent         []string
	Errors         []string
}

type Options struct {
	LogPath string
	Once    bool
	Limit   int
	Every   time.Duration
}

func NewModel() *Model {
	return &Model{
		WorkflowStatus: StatusPending,
		Nodes:          map[string]*NodeView{},
	}
}

func ReadModel(r io.Reader, recentLimit int) (*Model, error) {
	model := NewModel()
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}
		var event runlog.Event
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			model.Errors = append(model.Errors, fmt.Sprintf("line %d: %v", lineNo, err))
			continue
		}
		model.Apply(event, recentLimit)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return model, nil
}

func (m *Model) Apply(event runlog.Event, recentLimit int) {
	switch event.Type {
	case runlog.EventWorkflowStarted:
		m.WorkflowName = event.Message
		m.WorkflowStatus = StatusRunning
	case runlog.EventWorkflowPassed:
		m.WorkflowName = firstNonEmpty(m.WorkflowName, event.Message)
		m.WorkflowStatus = StatusPassed
	case runlog.EventWorkflowFailed:
		m.WorkflowStatus = StatusFailed
		m.appendRecent("workflow", event.Message, recentLimit)
	case runlog.EventNodeStarted:
		m.node(event.NodeID).Status = StatusRunning
	case runlog.EventNodePassed:
		m.node(event.NodeID).Status = StatusPassed
	case runlog.EventNodeFailed:
		node := m.node(event.NodeID)
		node.Status = StatusFailed
		node.Message = event.Message
		m.appendRecent(event.NodeID, event.Message, recentLimit)
	case runlog.EventNodeSkipped:
		node := m.node(event.NodeID)
		node.Status = StatusSkipped
		node.Message = event.Message
	case runlog.EventNodePaused:
		node := m.node(event.NodeID)
		node.Status = StatusPaused
		node.Message = event.Message
		m.WorkflowStatus = StatusPaused
		m.appendRecent(event.NodeID, event.Message, recentLimit)
	case runlog.EventNodeOutput:
		node := m.node(event.NodeID)
		node.Output = appendLimited(node.Output, event.Message, recentLimit)
		m.appendRecent(event.NodeID, event.Message, recentLimit)
	}
}

func (m *Model) Render(w io.Writer, recentLimit int) {
	name := firstNonEmpty(m.WorkflowName, "(unknown workflow)")
	fmt.Fprintf(w, "Micromage run dashboard\n")
	fmt.Fprintf(w, "Workflow: %s [%s]\n\n", name, m.WorkflowStatus)

	ids := make([]string, 0, len(m.Nodes))
	for id := range m.Nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		fmt.Fprintln(w, "Nodes: no events yet")
	} else {
		fmt.Fprintln(w, "Nodes:")
		for _, id := range ids {
			node := m.Nodes[id]
			line := fmt.Sprintf("  %-24s %s", node.ID, node.Status)
			if node.Message != "" {
				line += " - " + node.Message
			}
			fmt.Fprintln(w, line)
		}
	}

	fmt.Fprintln(w, "\nRecent output:")
	recent := m.Recent
	if recentLimit > 0 && len(recent) > recentLimit {
		recent = recent[len(recent)-recentLimit:]
	}
	if len(recent) == 0 {
		fmt.Fprintln(w, "  (none)")
	} else {
		for _, line := range recent {
			fmt.Fprintf(w, "  %s\n", line)
		}
	}

	if len(m.Errors) > 0 {
		fmt.Fprintln(w, "\nLog warnings:")
		for _, err := range m.Errors {
			fmt.Fprintf(w, "  %s\n", err)
		}
	}
}

func Run(ctx context.Context, w io.Writer, opts Options) error {
	if opts.LogPath == "" {
		return fmt.Errorf("log path is required")
	}
	if opts.Limit <= 0 {
		opts.Limit = 10
	}
	if opts.Every <= 0 {
		opts.Every = time.Second
	}
	for {
		if err := renderFile(w, opts.LogPath, opts.Limit, !opts.Once); err != nil {
			return err
		}
		if opts.Once {
			return nil
		}
		timer := time.NewTimer(opts.Every)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func renderFile(w io.Writer, path string, limit int, clear bool) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	model, err := ReadModel(file, limit)
	if err != nil {
		return err
	}
	if clear {
		// A simple ANSI refresh keeps active runs readable without adding a TUI dependency.
		fmt.Fprint(w, "\033[H\033[2J")
	}
	model.Render(w, limit)
	return nil
}

func (m *Model) node(id string) *NodeView {
	if id == "" {
		id = "(workflow)"
	}
	node, ok := m.Nodes[id]
	if !ok {
		node = &NodeView{ID: id, Status: StatusPending}
		m.Nodes[id] = node
	}
	return node
}

func (m *Model) appendRecent(nodeID, message string, limit int) {
	if strings.TrimSpace(message) == "" {
		return
	}
	prefix := firstNonEmpty(nodeID, "workflow")
	m.Recent = appendLimited(m.Recent, prefix+": "+message, limit)
}

func appendLimited(lines []string, line string, limit int) []string {
	lines = append(lines, line)
	if limit > 0 && len(lines) > limit {
		return append([]string(nil), lines[len(lines)-limit:]...)
	}
	return lines
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
