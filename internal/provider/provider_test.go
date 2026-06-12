package provider

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestRenderOpenCodePreset(t *testing.T) {
	inv, err := Render(Request{
		Name:       OpenCode,
		Dir:        "/repo",
		PromptFile: "/tmp/prompt.md",
		NodeID:     "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"run", "Follow the attached workflow node prompt. Do not ask interactive questions.", "--model", "opencode/deepseek-v4-flash-free", "--format", "json", "--dir", "/repo", "--file", "/tmp/prompt.md"}
	if inv.Binary != "opencode" {
		t.Fatalf("binary = %q", inv.Binary)
	}
	if !reflect.DeepEqual(inv.Args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", inv.Args, wantArgs)
	}
	assertEnvContains(t, inv.Env, "MICROMAGE_NODE_ID=agent", "MICROMAGE_PROVIDER=opencode", "CI=1", "TERM=dumb", "NO_COLOR=1")
}

func TestRenderCodexPresetWithModelAndBinaryOverride(t *testing.T) {
	inv, err := Render(Request{
		Name:   Codex,
		Binary: "/bin/codex",
		Model:  "gpt-5",
		Dir:    "/repo",
		Prompt: "summarize",
		NodeID: "review",
	})
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"exec", "--json", "--color", "never", "--sandbox", "danger-full-access", "--ask-for-approval", "never", "--skip-git-repo-check", "--cd", "/repo", "--model", "gpt-5", "summarize"}
	if inv.Binary != "/bin/codex" {
		t.Fatalf("binary = %q", inv.Binary)
	}
	if !reflect.DeepEqual(inv.Args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", inv.Args, wantArgs)
	}
	assertEnvContains(t, inv.Env, "MICROMAGE_PROVIDER=codex", "MICROMAGE_NODE_ID=review")
}

func TestRenderCodexOmitsEmptyModel(t *testing.T) {
	inv, err := Render(Request{Name: Codex, Dir: "/repo", Prompt: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	for i, arg := range inv.Args {
		if arg == "--model" {
			t.Fatalf("unexpected model flag at %d in %#v", i, inv.Args)
		}
	}
}

func TestCheckBinaryReportsProviderDiagnostic(t *testing.T) {
	inv, err := Render(Request{Name: OpenCode, Binary: "missing-opencode", Dir: "/repo", PromptFile: "/tmp/prompt.md"})
	if err != nil {
		t.Fatal(err)
	}
	err = CheckBinary(inv, func(string) (string, error) {
		return "", errors.New("not found")
	})
	if err == nil {
		t.Fatal("expected missing binary error")
	}
	msg := err.Error()
	for _, want := range []string{"provider \"opencode\"", "\"missing-opencode\"", "--provider-binary"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("missing %q in %q", want, msg)
		}
	}
}

func TestDiscoverIncludesOnlyInstalledKnownProvidersAndLocalAntigravity(t *testing.T) {
	found := Discover(func(binary string) (string, error) {
		switch binary {
		case "opencode", "antigravity":
			return "/bin/" + binary, nil
		default:
			return "", errors.New("not found")
		}
	})
	want := []string{"antigravity", "opencode"}
	if !reflect.DeepEqual(found, want) {
		t.Fatalf("found = %#v, want %#v", found, want)
	}
}

func assertEnvContains(t *testing.T, env []string, wants ...string) {
	t.Helper()
	got := "\n" + strings.Join(env, "\n") + "\n"
	for _, want := range wants {
		if !strings.Contains(got, "\n"+want+"\n") {
			t.Fatalf("env missing %q in %#v", want, env)
		}
	}
}
