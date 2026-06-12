package provider

import (
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

const (
	OpenCode = "opencode"
	Codex    = "codex"
)

type Request struct {
	Name       string
	Binary     string
	Model      string
	Dir        string
	Prompt     string
	PromptFile string
	NodeID     string
}

type Invocation struct {
	Provider string
	Binary   string
	Args     []string
	Env      []string
	Dir      string
	Stdin    string
}

type Preset struct {
	Name         string
	Binary       string
	DefaultModel string
	render       func(Request, Preset) (Invocation, error)
}

var presets = map[string]Preset{
	OpenCode: {
		Name:         OpenCode,
		Binary:       "opencode",
		DefaultModel: "opencode/deepseek-v4-flash-free",
		render:       renderOpenCode,
	},
	Codex: {
		Name:   Codex,
		Binary: "codex",
		render: renderCodex,
	},
}

func Supported() []string {
	names := make([]string, 0, len(presets))
	for name := range presets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func Discover(lookup func(string) (string, error)) []string {
	if lookup == nil {
		lookup = exec.LookPath
	}
	var found []string
	for _, name := range Supported() {
		preset := presets[name]
		if _, err := lookup(preset.Binary); err == nil {
			found = append(found, name)
		}
	}
	if _, err := lookup("antigravity"); err == nil {
		found = append(found, "antigravity")
	}
	sort.Strings(found)
	return found
}

func Render(req Request) (Invocation, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = OpenCode
	}
	preset, ok := presets[name]
	if !ok {
		return Invocation{}, fmt.Errorf("unknown provider preset %q (supported: %s)", name, strings.Join(Supported(), ", "))
	}
	if strings.TrimSpace(req.Binary) == "" {
		req.Binary = preset.Binary
	}
	if strings.TrimSpace(req.Model) == "" {
		req.Model = preset.DefaultModel
	}
	inv, err := preset.render(req, preset)
	if err != nil {
		return Invocation{}, err
	}
	inv.Provider = name
	inv.Dir = req.Dir
	// Provider presets keep AI CLIs noninteractive and make event logs attributable.
	inv.Env = append([]string{"CI=1", "TERM=dumb", "NO_COLOR=1", "MICROMAGE_NODE_ID=" + req.NodeID, "MICROMAGE_PROVIDER=" + name}, inv.Env...)
	return inv, nil
}

func CheckBinary(inv Invocation, lookup func(string) (string, error)) error {
	if lookup == nil {
		lookup = exec.LookPath
	}
	if _, err := lookup(inv.Binary); err != nil {
		return fmt.Errorf("provider %q binary %q was not found; install it or pass --provider-binary: %w", inv.Provider, inv.Binary, err)
	}
	return nil
}

func renderOpenCode(req Request, preset Preset) (Invocation, error) {
	if strings.TrimSpace(req.PromptFile) == "" {
		return Invocation{}, errors.New("opencode preset requires a prompt file")
	}
	args := []string{
		"run",
		"Follow the attached workflow node prompt. Do not ask interactive questions.",
		"--model", req.Model,
		"--format", "json",
		"--dir", req.Dir,
		"--file", req.PromptFile,
	}
	return Invocation{Binary: req.Binary, Args: args}, nil
}

func renderCodex(req Request, preset Preset) (Invocation, error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return Invocation{}, errors.New("codex preset requires prompt text")
	}
	args := []string{
		"exec",
		"--json",
		"--color", "never",
		"--sandbox", "danger-full-access",
		"--ask-for-approval", "never",
		"--skip-git-repo-check",
		"--cd", req.Dir,
	}
	if strings.TrimSpace(req.Model) != "" {
		args = append(args, "--model", req.Model)
	}
	args = append(args, req.Prompt)
	return Invocation{Binary: req.Binary, Args: args}, nil
}
