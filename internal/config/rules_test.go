package config

import (
	"strings"
	"testing"
	"time"

	"github.com/YasushiKobayashi/agent-dispatch/internal/model"
)

const validRules = `
version: 1
rules:
  - name: review-requested
    on: [review_requested]
    match:
      repos: ["YasushiKobayashi/*"]
      exclude_labels: ["wip"]
    action:
      prompt: |
        {{.Repo}}#{{.PRNumber}} ({{.BaseRefName}}) をレビューして
      timeout: 15m
  - name: disabled-one
    enabled: false
    on: [review_requested]
    action:
      prompt: "noop"
`

func TestLoadRules(t *testing.T) {
	path := writeFile(t, t.TempDir(), "rules.yaml", validRules)

	rules, err := LoadRules(path)
	if err != nil {
		t.Fatalf("LoadRules: %v", err)
	}
	if len(rules.Rules) != 2 {
		t.Fatalf("got %d rules, want 2", len(rules.Rules))
	}
	if enabled := rules.Enabled(); len(enabled) != 1 || enabled[0].Name != "review-requested" {
		t.Fatalf("Enabled() = %+v, want only review-requested", enabled)
	}

	first := rules.Rules[0]
	if !first.IsEnabled() {
		t.Error("rule with no `enabled` key should default to enabled")
	}
	if !first.ShouldNotify() {
		t.Error("action with no `notify` key should default to notifying")
	}
	if !first.Handles(model.KindReviewRequested) {
		t.Error("Handles(review_requested) = false, want true")
	}
	if got, want := first.Action.Timeout.D(), 15*time.Minute; got != want {
		t.Errorf("timeout = %v, want %v", got, want)
	}
}

func TestRenderPrompt(t *testing.T) {
	path := writeFile(t, t.TempDir(), "rules.yaml", validRules)
	rules, err := LoadRules(path)
	if err != nil {
		t.Fatalf("LoadRules: %v", err)
	}

	got, err := rules.Rules[0].RenderPrompt(model.Event{
		Repo:        "YasushiKobayashi/agent-dispatch",
		PRNumber:    12,
		BaseRefName: "main",
	})
	if err != nil {
		t.Fatalf("RenderPrompt: %v", err)
	}
	if want := "YasushiKobayashi/agent-dispatch#12 (main) をレビューして"; got != want {
		t.Errorf("RenderPrompt = %q, want %q", got, want)
	}
}

func TestRenderPromptOnHandBuiltRule(t *testing.T) {
	rule := Rule{Name: "adhoc", Action: Action{Prompt: "review {{.Repo}}"}}

	got, err := rule.RenderPrompt(model.Event{Repo: "o/n"})
	if err != nil {
		t.Fatalf("RenderPrompt: %v", err)
	}
	if got != "review o/n" {
		t.Errorf("RenderPrompt = %q, want %q", got, "review o/n")
	}
}

func TestLoadRulesRejectsBadInput(t *testing.T) {
	cases := map[string]struct {
		body string
		want string
	}{
		"no rules":         {"version: 1\nrules: []\n", "no rules defined"},
		"missing name":     {"version: 1\nrules:\n  - on: [review_requested]\n    action: {prompt: x}\n", "name is required"},
		"duplicate name":   {"version: 1\nrules:\n  - {name: a, on: [review_requested], action: {prompt: x}}\n  - {name: a, on: [review_requested], action: {prompt: y}}\n", "duplicate rule name"},
		"unknown kind":     {"version: 1\nrules:\n  - {name: a, on: [pr_merged], action: {prompt: x}}\n", "unknown event kind"},
		"empty on":         {"version: 1\nrules:\n  - {name: a, on: [], action: {prompt: x}}\n", "must list at least one event kind"},
		"missing prompt":   {"version: 1\nrules:\n  - {name: a, on: [review_requested], action: {}}\n", "action.prompt is required"},
		"bad template":     {"version: 1\nrules:\n  - {name: a, on: [review_requested], action: {prompt: \"{{.Repo\"}}\n", "not a valid template"},
		"bad version":      {"version: 2\nrules:\n  - {name: a, on: [review_requested], action: {prompt: x}}\n", "unsupported version"},
		"unknown yaml key": {"version: 1\nrules:\n  - {name: a, on: [review_requested], actoin: {prompt: x}}\n", "parse"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeFile(t, t.TempDir(), "rules.yaml", tc.body)
			_, err := LoadRules(path)
			if err == nil {
				t.Fatalf("LoadRules accepted %s, want an error", name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestLoadRulesMissingFileIsAnError(t *testing.T) {
	if _, err := LoadRules(t.TempDir() + "/nope.yaml"); err == nil {
		t.Fatal("LoadRules accepted a missing file, want an error")
	}
}

func TestRuleModeOverrideIsValidated(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "rules.yaml", `
version: 1
rules:
  - name: r
    on: [review_requested]
    action:
      prompt: "hi"
      mode: iterm
`)
	if _, err := LoadRules(path); err == nil {
		t.Fatal("LoadRules accepted an unknown action.mode")
	}
}

func TestRuleModeFallsBackToTheConfiguredDefault(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "rules.yaml", `
version: 1
rules:
  - name: default-mode
    on: [review_requested]
    action:
      prompt: "hi"
  - name: explicit-mode
    on: [review_requested]
    action:
      prompt: "hi"
      mode: ghostty
`)
	loaded, err := LoadRules(path)
	if err != nil {
		t.Fatalf("LoadRules: %v", err)
	}
	if got := loaded.Rules[0].ModeOr(ModeHeadless); got != ModeHeadless {
		t.Errorf("rule without mode = %q, want the configured default", got)
	}
	if got := loaded.Rules[1].ModeOr(ModeHeadless); got != ModeGhostty {
		t.Errorf("rule with mode = %q, want ghostty", got)
	}
}
