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
	"strings"
)

const DefaultOpenCodeModel = "opencode/nemotron-3-ultra-free"

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
		artifactsDir = filepath.Join(os.TempDir(), "micromage-runs", config.WorkflowID)
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

func (runner *RealRunner) RunNode(ctx context.Context, node Node, emit EventSink) error {
	if err := os.MkdirAll(runner.artifactsDir, 0o755); err != nil {
		return err
	}
	switch node.Kind() {
	case "prompt":
		return runner.runAI(ctx, node, node.Prompt, emit)
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
			runner.outputs[node.ID] = output
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
		runner.outputs[node.ID] = output
	}
	return err
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
	for id, output := range runner.outputs {
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
	return prompt
}

func (runner *RealRunner) substituteOutputs(script string) string {
	for id, output := range runner.outputs {
		var object map[string]any
		if json.Unmarshal([]byte(output), &object) == nil {
			for key, value := range object {
				script = strings.ReplaceAll(script, "$"+id+".output."+key, shellEscape(fmt.Sprint(value)))
			}
		}
		script = strings.ReplaceAll(script, "$"+id+".output", shellEscape(output))
	}
	return script
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
	return strings.TrimSpace(output.String()), nil
}

func scanOpenCode(stream io.Reader, nodeID string, emit EventSink, output *strings.Builder, errs chan<- error) {
	scanner := bufio.NewScanner(stream)
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
	var payload any
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		return line
	}
	if text, ok := findText(payload); ok {
		return text
	}
	return ""
}

func findText(value any) (string, bool) {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range []string{"delta", "text", "content"} {
			if text, ok := typed[key].(string); ok && text != "" {
				return text, true
			}
		}
		for _, child := range typed {
			if text, ok := findText(child); ok {
				return text, true
			}
		}
	case []any:
		for _, child := range typed {
			if text, ok := findText(child); ok {
				return text, true
			}
		}
	}
	return "", false
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
