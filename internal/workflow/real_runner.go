package workflow

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const DefaultOpenCodeModel = "opencode/nemotron-3-ultra-free"

const (
	// Workflow limits keep local real runs from turning logs or artifacts into unbounded memory pressure.
	maxBashStdoutBytes       = 1 * 1024 * 1024
	maxBashStderrBytes       = 1 * 1024 * 1024
	maxProviderLineBytes     = 1 * 1024 * 1024
	maxProviderOutputBytes   = 4 * 1024 * 1024
	maxNodeLogMessageBytes   = 256 * 1024
	maxDeclaredArtifactBytes = 4 * 1024 * 1024
)

// OpenCode 1.16.2 uses one local SQLite database, so provider calls stay serialized to avoid lock failures.
var openCodeMu sync.Mutex

type PromptRequest struct {
	Prompt       string
	CWD          string
	Model        string
	Node         Node
	Arguments    string
	WorkflowID   string
	ArtifactsDir string
}

type AIProvider interface {
	RunPrompt(context.Context, PromptRequest, EventSink) (string, error)
}

type ProviderRegistry map[string]AIProvider

type RealRunnerConfig struct {
	Commands        CommandRegistry
	Providers       ProviderRegistry
	CWD             string
	Arguments       string
	WorkflowID      string
	ArtifactsDir    string
	BaseBranch      string
	DefaultProvider string
	DefaultModel    string
	Unsafe          bool
}

type RealRunner struct {
	commands        CommandRegistry
	providers       ProviderRegistry
	cwd             string
	arguments       string
	workflowID      string
	artifactsDir    string
	baseBranch      string
	defaultProvider string
	defaultModel    string
	outputMu        sync.Mutex
	outputs         map[string]string
}

func NewRealRunner(config RealRunnerConfig) *RealRunner {
	providers := config.Providers
	if providers == nil {
		providers = ProviderRegistry{"opencode": OpenCodeProvider{Unsafe: config.Unsafe}}
	}
	cwd := config.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	artifactsDir := config.ArtifactsDir
	if artifactsDir == "" {
		artifactsDir = DefaultArtifactsDir(cwd, config.WorkflowID)
	}
	return &RealRunner{
		commands:        config.Commands,
		providers:       providers,
		cwd:             cwd,
		arguments:       config.Arguments,
		workflowID:      config.WorkflowID,
		artifactsDir:    artifactsDir,
		baseBranch:      config.BaseBranch,
		defaultProvider: config.DefaultProvider,
		defaultModel:    config.DefaultModel,
		outputs:         map[string]string{},
	}
}

func DefaultArtifactsDir(cwd string, workflowID string) string {
	if workflowID == "" {
		workflowID = "run"
	}
	// Agent-readable artifacts live under the repo so CLI providers can access them.
	return filepath.Join(cwd, ".micromage", "runs", workflowID)
}

func (runner *RealRunner) RunNode(ctx context.Context, node Node, emit EventSink) error {
	if err := os.MkdirAll(runner.artifactsDir, 0o755); err != nil {
		return err
	}
	if node.Kind() == "bash" {
		if timeout := nodeTimeout(node); timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
	}
	switch node.Kind() {
	case "prompt":
		return runner.runAI(ctx, node, runner.expandPrompt(node.Prompt), emit)
	case "command":
		command, ok := runner.commands[node.Command]
		if !ok {
			return fmt.Errorf("command %s was not found", node.Command)
		}
		prompt := runner.expandPrompt(command.Body)
		return runner.runAI(ctx, node, prompt, emit)
	case "bash":
		output, err := runner.runBash(ctx, node, emit)
		if err == nil {
			runner.setOutput(node.ID, output)
		}
		return err
	default:
		return fmt.Errorf("unsupported real node kind %q for node %s", node.Kind(), node.ID)
	}
}

func (runner *RealRunner) runAI(ctx context.Context, node Node, prompt string, emit EventSink) error {
	providerName := node.Provider
	if providerName == "" {
		providerName = runner.defaultProvider
	}
	if providerName == "" {
		providerName = "opencode"
	}
	provider, ok := runner.providers[providerName]
	if !ok {
		return fmt.Errorf("provider %s was not registered", providerName)
	}
	if !providerAppliesNodeTimeout(provider) {
		if timeout := nodeTimeout(node); timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
	}
	model := node.Model
	if model == "" {
		model = runner.defaultModel
	}
	if model == "" {
		model = DefaultOpenCodeModel
	}
	output, err := provider.RunPrompt(ctx, PromptRequest{
		Prompt:       prompt,
		CWD:          runner.cwd,
		Model:        model,
		Node:         node,
		Arguments:    runner.arguments,
		WorkflowID:   runner.workflowID,
		ArtifactsDir: runner.artifactsDir,
	}, emit)
	if err == nil {
		// Failed AI streams can include partial text, so only completed runs publish downstream outputs.
		if artifactOutput, artifactErr := runner.collectExpectedOutputs(node, output); artifactErr != nil {
			return artifactErr
		} else if artifactOutput != "" {
			output = artifactOutput
		}
		runner.setOutput(node.ID, output)
	}
	return err
}

func providerAppliesNodeTimeout(provider AIProvider) bool {
	switch provider.(type) {
	case OpenCodeProvider, *OpenCodeProvider:
		return true
	default:
		return false
	}
}

func nodeTimeout(node Node) time.Duration {
	if node.IdleTimeout != nil && *node.IdleTimeout > 0 {
		return time.Duration(*node.IdleTimeout) * time.Millisecond
	}
	if node.Timeout != nil && *node.Timeout > 0 {
		return time.Duration(*node.Timeout) * time.Second
	}
	return 0
}

func (runner *RealRunner) runBash(ctx context.Context, node Node, emit EventSink) (string, error) {
	script := runner.substituteOutputs(node.Bash)
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Dir = runner.cwd
	cmd.Env = runner.env()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}

	streamErrs := make(chan error, 2)
	go readLimitedProcessStream(stdoutPipe, "stdout", maxBashStdoutBytes, &stdout, streamErrs)
	go readLimitedProcessStream(stderrPipe, "stderr", maxBashStderrBytes, &stderr, streamErrs)

	waitErrs := make(chan error, 1)
	go func() {
		waitErrs <- cmd.Wait()
	}()

	var streamErr error
	var waitErr error
	streamsDone := 0
	waitDone := false
	for streamsDone < 2 || !waitDone {
		select {
		case err := <-streamErrs:
			streamsDone++
			if err != nil && streamErr == nil {
				streamErr = err
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
			}
		case err := <-waitErrs:
			waitDone = true
			waitErr = err
		}
	}
	if streamErr != nil {
		return stdout.String(), streamErr
	}
	if waitErr != nil {
		if text := strings.TrimSpace(stderr.String()); text != "" {
			if emitErr := emitBoundedNodeLog(node.ID, "bash stderr", text, emit); emitErr != nil {
				return stdout.String(), emitErr
			}
		}
		return stdout.String(), waitErr
	}
	output := strings.TrimRight(stdout.String(), "\n")
	if output != "" {
		if err := emitBoundedNodeLog(node.ID, "bash stdout", output, emit); err != nil {
			return output, err
		}
	}
	return output, nil
}

func readLimitedProcessStream(stream io.Reader, name string, limit int64, output *bytes.Buffer, errs chan<- error) {
	var total int64
	buf := make([]byte, 32*1024)
	for {
		n, err := stream.Read(buf)
		if n > 0 {
			total += int64(n)
			if total > limit {
				errs <- fmt.Errorf("bash %s exceeded limit of %d bytes", name, limit)
				return
			}
			_, _ = output.Write(buf[:n])
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				errs <- nil
				return
			}
			errs <- err
			return
		}
	}
}

func (runner *RealRunner) env() []string {
	env := append([]string{}, os.Environ()...)
	env = append(env,
		"ARGUMENTS="+runner.arguments,
		"WORKFLOW_ID="+runner.workflowID,
		"ARTIFACTS_DIR="+runner.artifactsDir,
		"BASE_BRANCH="+runner.baseBranch,
	)
	for id, output := range runner.snapshotOutputs() {
		env = append(env, "NODE_"+envName(id)+"_OUTPUT="+output)
	}
	return env
}

func (runner *RealRunner) expandPrompt(prompt string) string {
	replacements := map[string]string{
		"$ARGUMENTS":     runner.arguments,
		"$WORKFLOW_ID":   runner.workflowID,
		"$ARTIFACTS_DIR": runner.artifactsDir,
	}
	for key, value := range replacements {
		prompt = strings.ReplaceAll(prompt, key, value)
	}
	// Runtime context lets review agents consume collected scope without re-running setup.
	return runner.substitutePromptOutputs(prompt)
}

func (runner *RealRunner) substituteOutputs(script string) string {
	return runner.substituteOutputsWith(script, shellEscape)
}

func (runner *RealRunner) substitutePromptOutputs(prompt string) string {
	return runner.substituteOutputsWith(prompt, func(value string) string { return value })
}

func (runner *RealRunner) substituteOutputsWith(text string, formatValue func(string) string) string {
	outputs := runner.snapshotOutputs()
	ids := make([]string, 0, len(outputs))
	for id := range outputs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if len(ids[i]) == len(ids[j]) {
			return ids[i] < ids[j]
		}
		return len(ids[i]) > len(ids[j])
	})
	for _, id := range ids {
		output := outputs[id]
		var object map[string]any
		if json.Unmarshal([]byte(output), &object) == nil {
			keys := make([]string, 0, len(object))
			for key := range object {
				keys = append(keys, key)
			}
			sort.Slice(keys, func(i, j int) bool {
				if len(keys[i]) == len(keys[j]) {
					return keys[i] < keys[j]
				}
				return len(keys[i]) > len(keys[j])
			})
			for _, key := range keys {
				text = replaceOutputToken(text, "$"+id+".output."+key, formatValue(fmt.Sprint(object[key])), false)
			}
		}
		text = replaceOutputToken(text, "$"+id+".output", formatValue(output), true)
	}
	return text
}

func replaceOutputToken(text string, token string, value string, protectNested bool) string {
	var builder strings.Builder
	for {
		index := strings.Index(text, token)
		if index < 0 {
			builder.WriteString(text)
			return builder.String()
		}
		builder.WriteString(text[:index])
		after := index + len(token)
		if after < len(text) && isOutputTokenChar(rune(text[after]), protectNested) {
			builder.WriteString(token)
			text = text[after:]
			continue
		}
		builder.WriteString(value)
		text = text[after:]
	}
}

func isOutputTokenChar(char rune, protectNested bool) bool {
	if protectNested && char == '.' {
		return true
	}
	return char == '_' || char == '-' || (char >= '0' && char <= '9') || (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z')
}

func (runner *RealRunner) collectExpectedOutputs(node Node, providerOutput string) (string, error) {
	var collected []string
	paths := make([]string, 0, len(node.Outputs))
	for _, pattern := range node.Outputs {
		path, err := runner.resolveOutputPath(pattern)
		if err != nil {
			return "", fmt.Errorf("node %s has invalid declared output: %w", node.ID, err)
		}
		paths = append(paths, path)
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				if len(paths) == 1 && strings.TrimSpace(providerOutput) != "" {
					materialized := []byte(strings.TrimSpace(providerOutput) + "\n")
					if len(materialized) > maxDeclaredArtifactBytes {
						return "", fmt.Errorf("node %s declared output exceeds artifact read limit of %d bytes: %s", node.ID, maxDeclaredArtifactBytes, path)
					}
					if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
						return "", err
					}
					if err := os.WriteFile(path, materialized, 0o644); err != nil {
						return "", err
					}
					collected = append(collected, strings.TrimSpace(providerOutput))
					continue
				}
				return "", fmt.Errorf("node %s expected output was not written: %s", node.ID, path)
			}
			return "", err
		}
		if info.IsDir() {
			return "", fmt.Errorf("node %s expected output is a directory: %s", node.ID, path)
		}
		if info.Size() > maxDeclaredArtifactBytes {
			return "", fmt.Errorf("node %s declared output exceeds artifact read limit of %d bytes: %s", node.ID, maxDeclaredArtifactBytes, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		collected = append(collected, strings.TrimSpace(string(data)))
	}
	return strings.Join(collected, "\n\n"), nil
}

func (runner *RealRunner) resolveOutputPath(pattern string) (string, error) {
	path := runner.expandPrompt(pattern)
	return ResolveDeclaredArtifactPath(path, runner.artifactsDir)
}

func (runner *RealRunner) setOutput(id string, output string) {
	runner.outputMu.Lock()
	defer runner.outputMu.Unlock()
	// Parallel review nodes all publish artifacts into the same downstream context.
	runner.outputs[id] = output
}

func (runner *RealRunner) snapshotOutputs() map[string]string {
	runner.outputMu.Lock()
	defer runner.outputMu.Unlock()
	outputs := make(map[string]string, len(runner.outputs))
	for id, output := range runner.outputs {
		outputs[id] = output
	}
	return outputs
}

type OpenCodeProvider struct {
	Command string
	Unsafe  bool
}

func BuildOpenCodeArgs(model string, cwd string, prompt string, unsafe bool) []string {
	args := []string{"run", "--model", model, "--format", "json", "--dir", cwd}
	if unsafe {
		args = append(args, "--dangerously-skip-permissions")
	}
	return append(args, prompt)
}

func (provider OpenCodeProvider) RunPrompt(ctx context.Context, request PromptRequest, emit EventSink) (string, error) {
	openCodeMu.Lock()
	defer openCodeMu.Unlock()
	if timeout := nodeTimeout(request.Node); timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	command := provider.Command
	if command == "" {
		command = "opencode"
	}
	cmd := exec.CommandContext(ctx, command, BuildOpenCodeArgs(request.Model, request.CWD, request.Prompt, provider.Unsafe)...)
	cmd.Dir = request.CWD
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}

	var output strings.Builder
	errs := make(chan error, 2)
	go scanOpenCode(stdout, request.Node.ID, emit, &output, errs)
	go scanPlain(stderr, request.Node.ID, emit, errs)
	for i := 0; i < 2; i++ {
		if scanErr := <-errs; scanErr != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return output.String(), scanErr
		}
	}
	if err := cmd.Wait(); err != nil {
		return output.String(), err
	}
	result := strings.TrimSpace(output.String())
	if strings.Contains(result, "permission requested:") && strings.Contains(result, "auto-rejecting") {
		return result, fmt.Errorf("opencode permission auto-rejected for node %s", request.Node.ID)
	}
	if result == "" {
		return "", fmt.Errorf("opencode returned empty output for node %s", request.Node.ID)
	}
	return result, nil
}

func scanOpenCode(stream io.Reader, nodeID string, emit EventSink, output *strings.Builder, errs chan<- error) {
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 64*1024), maxProviderLineBytes)
	for scanner.Scan() {
		text := extractOpenCodeText(scanner.Text())
		if text == "" {
			continue
		}
		if err := appendProviderOutput(nodeID, output, text); err != nil {
			errs <- err
			return
		}
		if err := emitBoundedNodeLog(nodeID, "provider", text, emit); err != nil {
			errs <- err
			return
		}
	}
	errs <- providerScannerErr(scanner.Err(), "provider stdout")
}

func scanPlain(stream io.Reader, nodeID string, emit EventSink, errs chan<- error) {
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 64*1024), maxProviderLineBytes)
	for scanner.Scan() {
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		if err := emitBoundedNodeLog(nodeID, "provider stderr", text, emit); err != nil {
			errs <- err
			return
		}
	}
	errs <- providerScannerErr(scanner.Err(), "provider stderr")
}

func appendProviderOutput(nodeID string, output *strings.Builder, text string) error {
	if output.Len()+len(text) > maxProviderOutputBytes {
		return fmt.Errorf("provider output for node %s exceeded limit of %d bytes", nodeID, maxProviderOutputBytes)
	}
	output.WriteString(text)
	return nil
}

func emitBoundedNodeLog(nodeID string, source string, message string, emit EventSink) error {
	if len(message) > maxNodeLogMessageBytes {
		return fmt.Errorf("node %s %s log exceeded limit of %d bytes", nodeID, source, maxNodeLogMessageBytes)
	}
	return emit(RunEvent{Type: "node_log", NodeID: nodeID, Message: message})
}

func providerScannerErr(err error, source string) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "token too long") {
		return fmt.Errorf("%s line exceeded limit of %d bytes", source, maxProviderLineBytes)
	}
	return err
}

func extractOpenCodeText(line string) string {
	var payload struct {
		Type string `json:"type"`
		Part struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"part"`
	}
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		return line
	}
	if payload.Type == "text" && payload.Part.Type == "text" {
		return payload.Part.Text
	}
	return ""
}

func envName(value string) string {
	value = strings.ToUpper(value)
	replacer := strings.NewReplacer("-", "_", ".", "_", " ", "_")
	return replacer.Replace(value)
}

func shellEscape(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`")
	return replacer.Replace(value)
}

var _ AIProvider = OpenCodeProvider{}
