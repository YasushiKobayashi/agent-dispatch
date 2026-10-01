package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/goccy/go-yaml"

	"github.com/YasushiKobayashi/agent-dispatch/internal/model"
)

// Rules mirrors rules.yaml. See doc/design.md §4-2.
type Rules struct {
	Version int    `yaml:"version"`
	Rules   []Rule `yaml:"rules"`
}

// Rule binds a set of event kinds and a match expression to the Claude Code
// invocation that should follow.
type Rule struct {
	Name string `yaml:"name"`
	// Enabled is a pointer so that an omitted key means "enabled" while an
	// explicit `enabled: false` disables the rule.
	Enabled *bool        `yaml:"enabled"`
	On      []model.Kind `yaml:"on"`
	Match   Match        `yaml:"match"`
	Action  Action       `yaml:"action"`

	prompt  *template.Template
	workdir *template.Template
}

// Match narrows which pull requests a rule applies to. Empty list fields mean
// "no constraint"; the exclude_* fields always win over their allow counterpart.
type Match struct {
	Repos          []string `yaml:"repos"`
	ExcludeRepos   []string `yaml:"exclude_repos"`
	Authors        []string `yaml:"authors"`
	ExcludeAuthors []string `yaml:"exclude_authors"`
	Labels         []string `yaml:"labels"`
	ExcludeLabels  []string `yaml:"exclude_labels"`
	IncludeDraft   bool     `yaml:"include_draft"`
}

// Action describes what to run once a rule matches.
type Action struct {
	Prompt string `yaml:"prompt"`
	// WorkDir, when set, is where Claude Code starts, and reviewd prepares no
	// worktree of its own. It is a template over the event, like Prompt.
	//
	// The reason to point it at an existing clone rather than an isolated
	// worktree is Claude Code's own configuration: MCP servers added with the
	// default `local` scope, and the approval of a repository's .mcp.json, are
	// both keyed by the directory the session starts in. A fresh worktree path
	// has neither, so it loses every configured server and re-asks for
	// approval on every run. Starting in the clone keeps them, and the prompt
	// is what moves the work into a worktree.
	WorkDir string `yaml:"workdir"`
	// Mode overrides config.yaml's claude.mode for this rule. Empty means
	// "use the configured default".
	Mode    string   `yaml:"mode"`
	Timeout Duration `yaml:"timeout"`
	// Notify is a pointer for the same reason as Rule.Enabled.
	Notify       *bool `yaml:"notify"`
	KeepWorktree bool  `yaml:"keep_worktree"`
}

// ModeOr returns the rule's run mode, falling back to the configured default.
func (r Rule) ModeOr(def string) string {
	if strings.TrimSpace(r.Action.Mode) == "" {
		return def
	}
	return strings.ToLower(strings.TrimSpace(r.Action.Mode))
}

// IsEnabled reports whether the rule should be considered during matching.
func (r Rule) IsEnabled() bool { return r.Enabled == nil || *r.Enabled }

// ShouldNotify reports whether a completed run should raise a notification.
func (r Rule) ShouldNotify() bool { return r.Action.Notify == nil || *r.Action.Notify }

// Handles reports whether the rule listens for the given event kind.
func (r Rule) Handles(k model.Kind) bool {
	for _, on := range r.On {
		if on == k {
			return true
		}
	}
	return false
}

// RenderWorkDir expands the rule's workdir template against the event. It
// returns an empty string when the rule does not set one.
func (r Rule) RenderWorkDir(ev model.Event) (string, error) {
	if strings.TrimSpace(r.Action.WorkDir) == "" {
		return "", nil
	}
	tmpl := r.workdir
	if tmpl == nil {
		compiled, err := compileTemplate(r.Name, "action.workdir", r.Action.WorkDir)
		if err != nil {
			return "", err
		}
		tmpl = compiled
	}
	var sb strings.Builder
	if err := tmpl.Execute(&sb, ev); err != nil {
		return "", fmt.Errorf("rule %q: render workdir: %w", r.Name, err)
	}
	return ExpandPath(sb.String())
}

// RenderPrompt expands the rule's prompt template against the event.
func (r Rule) RenderPrompt(ev model.Event) (string, error) {
	tmpl := r.prompt
	if tmpl == nil {
		// Rules normally come from LoadRules, which compiles the template up
		// front. A nil template means the Rule was constructed by hand.
		compiled, err := compilePrompt(r.Name, r.Action.Prompt)
		if err != nil {
			return "", err
		}
		tmpl = compiled
	}
	var sb strings.Builder
	if err := tmpl.Execute(&sb, ev); err != nil {
		return "", fmt.Errorf("rule %q: render prompt: %w", r.Name, err)
	}
	rendered := strings.TrimSpace(sb.String())
	if rendered == "" {
		return "", fmt.Errorf("rule %q: prompt rendered empty", r.Name)
	}
	return rendered, nil
}

// LoadRules reads and validates rules.yaml. Unlike config.yaml, a missing
// rules.yaml is an error: without rules reviewd has nothing to do.
func LoadRules(path string) (Rules, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Rules{}, fmt.Errorf("read %s: %w", path, err)
	}

	var rules Rules
	if err := yaml.UnmarshalWithOptions(raw, &rules, yaml.Strict()); err != nil {
		return Rules{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if rules.Version == 0 {
		rules.Version = 1
	}
	if err := rules.validate(); err != nil {
		return Rules{}, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return rules, nil
}

// Enabled returns only the rules that are switched on.
func (r Rules) Enabled() []Rule {
	out := make([]Rule, 0, len(r.Rules))
	for _, rule := range r.Rules {
		if rule.IsEnabled() {
			out = append(out, rule)
		}
	}
	return out
}

func (r *Rules) validate() error {
	if r.Version != 1 {
		return fmt.Errorf("unsupported version %d (supported: 1)", r.Version)
	}
	if len(r.Rules) == 0 {
		return fmt.Errorf("no rules defined")
	}

	seen := make(map[string]struct{}, len(r.Rules))
	for i := range r.Rules {
		rule := &r.Rules[i]

		name := strings.TrimSpace(rule.Name)
		if name == "" {
			return fmt.Errorf("rules[%d]: name is required", i)
		}
		rule.Name = name
		if _, dup := seen[name]; dup {
			return fmt.Errorf("rules[%d]: duplicate rule name %q", i, name)
		}
		seen[name] = struct{}{}

		if len(rule.On) == 0 {
			return fmt.Errorf("rule %q: `on` must list at least one event kind", name)
		}
		for _, kind := range rule.On {
			if !kind.Valid() {
				return fmt.Errorf("rule %q: unknown event kind %q (known: %s)", name, kind, kindList())
			}
		}

		if strings.TrimSpace(rule.Action.Prompt) == "" {
			return fmt.Errorf("rule %q: action.prompt is required", name)
		}
		tmpl, err := compilePrompt(name, rule.Action.Prompt)
		if err != nil {
			return err
		}
		rule.prompt = tmpl

		if strings.TrimSpace(rule.Action.WorkDir) != "" {
			wd, werr := compileTemplate(name, "action.workdir", rule.Action.WorkDir)
			if werr != nil {
				return werr
			}
			rule.workdir = wd
		}

		if rule.Action.Timeout < 0 {
			return fmt.Errorf("rule %q: action.timeout must not be negative", name)
		}

		if mode := strings.TrimSpace(rule.Action.Mode); mode != "" {
			rule.Action.Mode = strings.ToLower(mode)
			if !ValidMode(rule.Action.Mode) {
				return fmt.Errorf("rule %q: action.mode %q is not one of %s",
					name, rule.Action.Mode, strings.Join(AllModes, ", "))
			}
		}
	}
	return nil
}

func compilePrompt(ruleName, prompt string) (*template.Template, error) {
	return compileTemplate(ruleName, "action.prompt", prompt)
}

func compileTemplate(ruleName, field, body string) (*template.Template, error) {
	tmpl, err := template.New(ruleName).Option("missingkey=error").Parse(body)
	if err != nil {
		return nil, fmt.Errorf("rule %q: %s is not a valid template: %w", ruleName, field, err)
	}
	return tmpl, nil
}

func kindList() string {
	names := make([]string, 0, len(model.AllKinds))
	for _, k := range model.AllKinds {
		names = append(names, string(k))
	}
	return strings.Join(names, ", ")
}
