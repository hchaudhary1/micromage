package runregistry

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const SchemaVersion = 1

type Status string

const (
	StatusRunning Status = "running"
	StatusPassed  Status = "passed"
	StatusFailed  Status = "failed"
	StatusPaused  Status = "paused"
	StatusStale   Status = "stale"
)

var ErrNotFound = errors.New("run registry entry not found")

type PIDLiveness func(pid int) bool

type Metadata struct {
	SchemaVersion  int       `json:"schema_version"`
	RunID          string    `json:"run_id"`
	WorkflowName   string    `json:"workflow_name"`
	WorkflowPath   string    `json:"workflow_path"`
	Workdir        string    `json:"workdir"`
	LogPath        string    `json:"log_path"`
	StatePath      string    `json:"state_path"`
	ProcessLogPath string    `json:"process_log_path"`
	OriginalArgv   []string  `json:"original_argv"`
	ChildArgv      []string  `json:"child_argv"`
	PID            int       `json:"pid"`
	Status         Status    `json:"status"`
	StartedAt      time.Time `json:"started_at"`
	EndedAt        time.Time `json:"ended_at"`
	ExitCode       int       `json:"exit_code"`
	Error          string    `json:"error"`
}

type Paths struct {
	Dir            string
	MetadataPath   string
	LogPath        string
	StatePath      string
	ProcessLogPath string
}

var idCounter uint64

func GenerateRunID() (string, error) {
	var random [6]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	n := atomic.AddUint64(&idCounter, 1)
	return fmt.Sprintf("%s-%d-%s", time.Now().UTC().Format("20060102T150405.000000000Z"), n, hex.EncodeToString(random[:])), nil
}

func DefaultPaths(workdir, runID string) Paths {
	dir := filepath.Join(workdir, ".micromage", "runs", runID)
	return Paths{
		Dir:            dir,
		MetadataPath:   filepath.Join(dir, "run.json"),
		LogPath:        filepath.Join(dir, "run.jsonl"),
		StatePath:      filepath.Join(dir, "state.json"),
		ProcessLogPath: filepath.Join(dir, "process.log"),
	}
}

func NewMetadata(runID, workflowName, workflowPath, workdir string, originalArgv, childArgv []string, pid int, startedAt time.Time) Metadata {
	paths := DefaultPaths(workdir, runID)
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	// Detached run metadata lets follow-up CLI commands find logs, state, and process ownership.
	return Metadata{
		SchemaVersion:  SchemaVersion,
		RunID:          runID,
		WorkflowName:   workflowName,
		WorkflowPath:   workflowPath,
		Workdir:        workdir,
		LogPath:        paths.LogPath,
		StatePath:      paths.StatePath,
		ProcessLogPath: paths.ProcessLogPath,
		OriginalArgv:   append([]string(nil), originalArgv...),
		ChildArgv:      append([]string(nil), childArgv...),
		PID:            pid,
		Status:         StatusRunning,
		StartedAt:      startedAt.UTC(),
	}
}

func Save(path string, metadata Metadata) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	bytes, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	bytes = append(bytes, '\n')
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

func Load(path string, isAlive PIDLiveness) (Metadata, error) {
	bytes, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Metadata{}, ErrNotFound
		}
		return Metadata{}, err
	}
	var metadata Metadata
	if err := json.Unmarshal(bytes, &metadata); err != nil {
		return Metadata{}, fmt.Errorf("parse run metadata: %w", err)
	}
	return applyStaleStatus(metadata, isAlive), nil
}

func List(workdir string, isAlive PIDLiveness) ([]Metadata, error) {
	root := filepath.Join(workdir, ".micromage", "runs")
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	runs := make([]Metadata, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		metadata, err := Load(filepath.Join(root, entry.Name(), "run.json"), isAlive)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
		runs = append(runs, metadata)
	}
	sort.SliceStable(runs, func(i, j int) bool {
		if runs[i].StartedAt.Equal(runs[j].StartedAt) {
			return strings.Compare(runs[i].RunID, runs[j].RunID) > 0
		}
		return runs[i].StartedAt.After(runs[j].StartedAt)
	})
	return runs, nil
}

func Latest(workdir string, isAlive PIDLiveness) (Metadata, error) {
	runs, err := List(workdir, isAlive)
	if err != nil {
		return Metadata{}, err
	}
	if len(runs) == 0 {
		return Metadata{}, ErrNotFound
	}
	return runs[0], nil
}

func DefaultPIDLiveness(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func applyStaleStatus(metadata Metadata, isAlive PIDLiveness) Metadata {
	if metadata.Status != StatusRunning {
		return metadata
	}
	if isAlive == nil {
		isAlive = DefaultPIDLiveness
	}
	if !isAlive(metadata.PID) {
		metadata.Status = StatusStale
	}
	return metadata
}
