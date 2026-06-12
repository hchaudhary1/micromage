package workflow

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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
const maxOpenCodeTokenSize = 8 * 1024 * 1024

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
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if text := strings.TrimSpace(stderr.String()); text != "" {
			_ = emit(RunEvent{Type: "node_log", NodeID: node.ID, Message: text})
		}
		return stdout.String(), err
	}
	output := strings.TrimRight(stdout.String(), "\n")
	if output != "" {
		if err := emit(RunEvent{Type: "node_log", NodeID: node.ID, Message: output}); err != nil {
			return output, err
		}
	}
	return output, nil
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
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				if len(paths) == 1 && strings.TrimSpace(providerOutput) != "" {
					if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
						return "", err
					}
					if err := os.WriteFile(path, []byte(strings.TrimSpace(providerOutput)+"\n"), 0o644); err != nil {
						return "", err
					}
					collected = append(collected, strings.TrimSpace(providerOutput))
					continue
				}
				return "", fmt.Errorf("node %s expected output was not written: %s", node.ID, path)
			}
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
	scanner.Buffer(make([]byte, 64*1024), maxOpenCodeTokenSize)
	for scanner.Scan() {
		text := extractOpenCodeText(scanner.Text())
		if text == "" {
			continue
		}
		output.WriteString(text)
		if err := emit(RunEvent{Type: "node_log", NodeID: nodeID, Message: text}); err != nil {
			errs <- err
			return
		}
	}
	errs <- scanner.Err()
}

func scanPlain(stream io.Reader, nodeID string, emit EventSink, errs chan<- error) {
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 64*1024), maxOpenCodeTokenSize)
	for scanner.Scan() {
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		if err := emit(RunEvent{Type: "node_log", NodeID: nodeID, Message: text}); err != nil {
			errs <- err
			return
		}
	}
	errs <- scanner.Err()
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
