// Package config loads and validates reviewd's two configuration files:
// config.yaml (process-wide settings) and rules.yaml (what to run, and when).
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
)

// DefaultDirName is the directory under the user's config home that holds
// config.yaml and rules.yaml.
const DefaultDirName = "reviewd"

// Config mirrors config.yaml. See doc/design.md §4-1.
type Config struct {
	Version int           `yaml:"version"`
	Paths   PathsConfig   `yaml:"paths"`
	GitHub  GitHubConfig  `yaml:"github"`
	Claude  ClaudeConfig  `yaml:"claude"`
	Ghostty GhosttyConfig `yaml:"ghostty"`
	Notify  NotifyConfig  `yaml:"notify"`
	Poll    PollConfig    `yaml:"poll"`
}

// PathsConfig locates reviewd's mutable state. Every field may be left empty,
// in which case it is derived from WorkDir.
type PathsConfig struct {
	WorkDir  string `yaml:"work_dir"`
	DBPath   string `yaml:"db_path"`
	ReposDir string `yaml:"repos_dir"`
	TreesDir string `yaml:"worktrees_dir"`
	RunsDir  string `yaml:"runs_dir"`
	LogsDir  string `yaml:"logs_dir"`
	LockPath string `yaml:"lock_path"`
}

// GitHubConfig configures how we talk to GitHub. Everything goes through the
// gh CLI so reviewd inherits the user's existing authentication.
type GitHubConfig struct {
	GhPath string `yaml:"gh_path"`
	// Login is the GitHub handle whose review requests we watch. Empty means
	// "resolve it at runtime via `gh api user`".
	Login string `yaml:"login"`
}

// Run modes. A rule may override the configured default per rule.
const (
	// ModeHeadless runs `claude -p` as a child process, waits for it, and
	// writes the answer to the run directory.
	ModeHeadless = "headless"
	// ModeGhostty opens a Ghostty tab running Claude Code interactively and
	// returns immediately. reviewd never sees the output: reading it is the
	// user's job, which is the whole point of the mode.
	ModeGhostty = "ghostty"
)

// AllModes lists every supported run mode.
var AllModes = []string{ModeHeadless, ModeGhostty}

// DefaultModel pins a session to Claude Code's "Default (recommended)" model.
// Passing it explicitly is the point: --model on the command line outranks a
// settings.json `model` and the ANTHROPIC_MODEL environment variable, so an
// unattended run cannot be silently moved onto a pricier model by ambient
// configuration it never sees.
const DefaultModel = "default"

// ClaudeConfig configures how Claude Code is invoked.
type ClaudeConfig struct {
	Path string `yaml:"path"`
	// Model is passed as --model to every launch. Empty means "pass nothing"
	// and inherit whatever the surrounding configuration selects.
	Model string `yaml:"model"`
	// Mode is the default run mode, one of AllModes.
	Mode          string   `yaml:"mode"`
	Timeout       Duration `yaml:"timeout"`
	MaxConcurrent int      `yaml:"max_concurrent"`
	// ExtraArgs are passed to the headless `claude -p` invocation.
	ExtraArgs []string `yaml:"extra_args"`
	// InteractiveArgs are passed to the interactive invocation used by
	// ModeGhostty. The headless-only output flags do not apply there.
	InteractiveArgs []string `yaml:"interactive_args"`
	// RemoteControl starts interactive sessions with Remote Control enabled, so
	// a review that reviewd opened on this Mac can be driven from elsewhere.
	// A pointer so an omitted key means "on" while `false` switches it off.
	// It has no effect in ModeHeadless: `claude -p` has no session to control.
	RemoteControl *bool `yaml:"remote_control"`
}

// ShouldRemoteControl reports whether interactive sessions get Remote Control.
func (c ClaudeConfig) ShouldRemoteControl() bool {
	return c.RemoteControl == nil || *c.RemoteControl
}

// GhosttyConfig configures the Ghostty tab opened by ModeGhostty.
type GhosttyConfig struct {
	AppPath string `yaml:"app_path"`
	// Window is "front" (add a tab to the frontmost window) or "new".
	Window string `yaml:"window"`
	// Activate brings the new tab to the foreground. Off by default so a
	// detected review does not steal focus mid-typing.
	Activate bool `yaml:"activate"`
	// KeepOpen leaves the tab open after Claude Code exits.
	KeepOpen *bool `yaml:"keep_open"`
}

// ShouldKeepOpen reports whether the tab outlives the command.
func (g GhosttyConfig) ShouldKeepOpen() bool { return g.KeepOpen == nil || *g.KeepOpen }

// NotifyConfig configures the macOS notification that fires when a run ends.
type NotifyConfig struct {
	Enabled                bool `yaml:"enabled"`
	PreferTerminalNotifier bool `yaml:"prefer_terminal_notifier"`
}

// PollConfig bounds a single `reviewd poll` invocation.
type PollConfig struct {
	MaxJobsPerPoll int      `yaml:"max_jobs_per_poll"`
	LockTimeout    Duration `yaml:"lock_timeout"`
}

// Default returns the configuration used when config.yaml is absent, and the
// base that a partial config.yaml is merged onto.
func Default() Config {
	return Config{
		Version: 1,
		Paths:   PathsConfig{WorkDir: "~/.reviewd"},
		GitHub:  GitHubConfig{GhPath: "gh"},
		Claude: ClaudeConfig{
			Path:          "claude",
			Model:         DefaultModel,
			Mode:          ModeHeadless,
			Timeout:       Duration(20 * time.Minute),
			MaxConcurrent: 1,
			// Read-only by construction: the allow list is what Claude Code may
			// use, and the deny list makes the intent explicit for anything a
			// future default might otherwise let through. `--permission-mode
			// plan` is deliberately not used: it makes Claude propose a plan
			// instead of producing the review we asked for.
			ExtraArgs: []string{
				"--output-format", "stream-json", "--verbose",
				"--allowedTools", "Read,Grep,Glob,Bash(git diff:*),Bash(git log:*),Bash(git show:*),Bash(git status:*),Bash(git rev-parse:*)",
				"--disallowedTools", "Edit,Write,NotebookEdit,Bash(git push:*),Bash(git commit:*),Bash(gh:*)",
			},
			// Deliberately empty. The headless lock-down exists because `-p`
			// cannot ask: an unlisted tool is silently refused. Interactive
			// sessions have a human to answer the permission prompt, which is
			// both safer (it is a decision, not a static list) and less
			// limiting -- a hard `--disallowedTools` cannot be overridden at
			// runtime, so pre-denying Write or gh breaks any prompt that asks
			// Claude Code to keep a task file or read the pull request.
			InteractiveArgs: nil,
		},
		Ghostty: GhosttyConfig{AppPath: "/Applications/Ghostty.app", Window: "front"},
		Notify:  NotifyConfig{Enabled: true, PreferTerminalNotifier: true},
		Poll:    PollConfig{MaxJobsPerPoll: 3, LockTimeout: Duration(5 * time.Second)},
	}
}

// DefaultDir returns the directory holding config.yaml and rules.yaml,
// honouring XDG_CONFIG_HOME.
func DefaultDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config dir: %w", err)
	}
	return filepath.Join(base, DefaultDirName), nil
}

// Load reads config.yaml from path. A missing file is not an error: the
// defaults are returned so reviewd works out of the box with only rules.yaml.
func Load(path string) (Config, error) {
	cfg := Default()

	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// fall through with defaults
	case err != nil:
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	default:
		// Decoding onto the defaults leaves absent keys untouched, so a config
		// file only needs to state what it overrides.
		if derr := yaml.UnmarshalWithOptions(raw, &cfg, yaml.Strict()); derr != nil {
			return Config{}, fmt.Errorf("parse %s: %w", path, derr)
		}
	}

	if err := cfg.normalize(); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// normalize expands ~, derives the empty paths from WorkDir, and fills in any
// field the file explicitly blanked out.
func (c *Config) normalize() error {
	if c.Version == 0 {
		c.Version = 1
	}
	def := Default()

	if strings.TrimSpace(c.Paths.WorkDir) == "" {
		c.Paths.WorkDir = def.Paths.WorkDir
	}
	work, err := ExpandPath(c.Paths.WorkDir)
	if err != nil {
		return err
	}
	c.Paths.WorkDir = work

	derived := map[*string]string{
		&c.Paths.DBPath:   filepath.Join(work, "state.db"),
		&c.Paths.ReposDir: filepath.Join(work, "repos"),
		&c.Paths.TreesDir: filepath.Join(work, "worktrees"),
		&c.Paths.RunsDir:  filepath.Join(work, "runs"),
		&c.Paths.LogsDir:  filepath.Join(work, "logs"),
		&c.Paths.LockPath: filepath.Join(work, "reviewd.lock"),
	}
	for field, fallback := range derived {
		if strings.TrimSpace(*field) == "" {
			*field = fallback
			continue
		}
		expanded, eerr := ExpandPath(*field)
		if eerr != nil {
			return eerr
		}
		*field = expanded
	}

	if strings.TrimSpace(c.GitHub.GhPath) == "" {
		c.GitHub.GhPath = def.GitHub.GhPath
	}
	if strings.TrimSpace(c.Claude.Path) == "" {
		c.Claude.Path = def.Claude.Path
	}
	if strings.TrimSpace(c.Claude.Mode) == "" {
		c.Claude.Mode = def.Claude.Mode
	}
	c.Claude.Model = strings.TrimSpace(c.Claude.Model)
	c.Claude.Mode = strings.ToLower(strings.TrimSpace(c.Claude.Mode))
	if strings.TrimSpace(c.Ghostty.AppPath) == "" {
		c.Ghostty.AppPath = def.Ghostty.AppPath
	}
	if strings.TrimSpace(c.Ghostty.Window) == "" {
		c.Ghostty.Window = def.Ghostty.Window
	}
	c.Ghostty.Window = strings.ToLower(strings.TrimSpace(c.Ghostty.Window))
	if c.Claude.Timeout <= 0 {
		c.Claude.Timeout = def.Claude.Timeout
	}
	if c.Claude.MaxConcurrent <= 0 {
		c.Claude.MaxConcurrent = def.Claude.MaxConcurrent
	}
	if c.Poll.MaxJobsPerPoll <= 0 {
		c.Poll.MaxJobsPerPoll = def.Poll.MaxJobsPerPoll
	}
	if c.Poll.LockTimeout <= 0 {
		c.Poll.LockTimeout = def.Poll.LockTimeout
	}
	return nil
}

// Validate reports configuration that would fail at runtime in a confusing way.
func (c Config) Validate() error {
	if c.Version != 1 {
		return fmt.Errorf("config: unsupported version %d (supported: 1)", c.Version)
	}
	if !filepath.IsAbs(c.Paths.WorkDir) {
		return fmt.Errorf("config: paths.work_dir must resolve to an absolute path, got %q", c.Paths.WorkDir)
	}
	if !ValidMode(c.Claude.Mode) {
		return fmt.Errorf("config: claude.mode %q is not one of %s", c.Claude.Mode, strings.Join(AllModes, ", "))
	}
	if c.Ghostty.Window != "front" && c.Ghostty.Window != "new" {
		return fmt.Errorf("config: ghostty.window %q is not one of front, new", c.Ghostty.Window)
	}
	return nil
}

// ValidMode reports whether mode is a supported run mode.
func ValidMode(mode string) bool { return slices.Contains(AllModes, mode) }

// ExpandPath resolves a leading ~ against the user's home directory and makes
// the result absolute. launchd starts processes with a bare environment, so
// relying on shell expansion is not an option.
func ExpandPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", nil
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand %q: %w", p, err)
		}
		p = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", p, err)
	}
	return abs, nil
}
