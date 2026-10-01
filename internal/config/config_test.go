package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Claude.Path != "claude" {
		t.Errorf("claude.path = %q, want %q", cfg.Claude.Path, "claude")
	}
	if got, want := cfg.Claude.Timeout.D(), 20*time.Minute; got != want {
		t.Errorf("claude.timeout = %v, want %v", got, want)
	}
	if !filepath.IsAbs(cfg.Paths.WorkDir) {
		t.Errorf("work_dir = %q, want absolute", cfg.Paths.WorkDir)
	}
	if !cfg.Notify.Enabled {
		t.Error("notify.enabled = false, want true by default")
	}
}

func TestLoadDerivesPathsFromWorkDir(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.yaml", `
version: 1
paths:
  work_dir: `+dir+`/state
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := filepath.Join(dir, "state")
	for name, got := range map[string]string{
		"db_path":       cfg.Paths.DBPath,
		"repos_dir":     cfg.Paths.ReposDir,
		"worktrees_dir": cfg.Paths.TreesDir,
		"runs_dir":      cfg.Paths.RunsDir,
		"logs_dir":      cfg.Paths.LogsDir,
		"lock_path":     cfg.Paths.LockPath,
	} {
		if !strings.HasPrefix(got, want+string(filepath.Separator)) {
			t.Errorf("%s = %q, want it derived under %q", name, got, want)
		}
	}
}

func TestLoadPartialOverrideKeepsOtherDefaults(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.yaml", `
version: 1
claude:
  timeout: 90s
  max_concurrent: 4
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.Claude.Timeout.D(), 90*time.Second; got != want {
		t.Errorf("timeout = %v, want %v", got, want)
	}
	if cfg.Claude.MaxConcurrent != 4 {
		t.Errorf("max_concurrent = %d, want 4", cfg.Claude.MaxConcurrent)
	}
	if cfg.Claude.Path != "claude" {
		t.Errorf("claude.path = %q, want the default to survive", cfg.Claude.Path)
	}
	if len(cfg.Claude.ExtraArgs) == 0 {
		t.Error("extra_args was emptied, want the default to survive")
	}
}

func TestLoadRejectsUnknownKey(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.yaml", "version: 1\nclaude:\n  timeuot: 5m\n")

	if _, err := Load(path); err == nil {
		t.Fatal("Load accepted a typo'd key, want an error")
	}
}

func TestLoadRejectsUnsupportedVersion(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.yaml", "version: 99\n")

	if _, err := Load(path); err == nil {
		t.Fatal("Load accepted version 99, want an error")
	}
}

func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}

	got, err := ExpandPath("~/foo/bar")
	if err != nil {
		t.Fatalf("ExpandPath: %v", err)
	}
	if want := filepath.Join(home, "foo", "bar"); got != want {
		t.Errorf("ExpandPath(~/foo/bar) = %q, want %q", got, want)
	}

	if got, err := ExpandPath(""); err != nil || got != "" {
		t.Errorf("ExpandPath(\"\") = %q, %v; want \"\", nil", got, err)
	}
}

func TestDurationAcceptsStringAndSeconds(t *testing.T) {
	dir := t.TempDir()

	path := writeFile(t, dir, "a.yaml", "version: 1\nclaude:\n  timeout: 2m30s\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.Claude.Timeout.D(), 150*time.Second; got != want {
		t.Errorf("timeout = %v, want %v", got, want)
	}

	path = writeFile(t, dir, "b.yaml", "version: 1\nclaude:\n  timeout: 45\n")
	cfg, err = Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.Claude.Timeout.D(), 45*time.Second; got != want {
		t.Errorf("timeout = %v, want %v", got, want)
	}

	path = writeFile(t, dir, "c.yaml", "version: 1\nclaude:\n  timeout: nonsense\n")
	if _, err := Load(path); err == nil {
		t.Fatal("Load accepted an unparseable duration, want an error")
	}
}

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestLoadDefaultsToHeadlessMode(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Claude.Mode != ModeHeadless {
		t.Errorf("claude.mode = %q, want %q", cfg.Claude.Mode, ModeHeadless)
	}
	if cfg.Ghostty.Window != "front" {
		t.Errorf("ghostty.window = %q, want front", cfg.Ghostty.Window)
	}
	// Absent means "keep the tab", so a command that fails instantly still
	// leaves its error on screen.
	if !cfg.Ghostty.ShouldKeepOpen() {
		t.Error("ghostty.keep_open defaulted to false, want true")
	}
}

func TestLoadNormalizesAndAcceptsGhosttyMode(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.yaml", "version: 1\nclaude:\n  mode: Ghostty\nghostty:\n  window: NEW\n  keep_open: false\n")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Claude.Mode != ModeGhostty {
		t.Errorf("claude.mode = %q, want %q", cfg.Claude.Mode, ModeGhostty)
	}
	if cfg.Ghostty.Window != "new" {
		t.Errorf("ghostty.window = %q, want new", cfg.Ghostty.Window)
	}
	if cfg.Ghostty.ShouldKeepOpen() {
		t.Error("explicit keep_open: false was ignored")
	}
}

// A typo'd mode must fail at load time, not silently fall through to headless
// on every poll.
func TestLoadRejectsUnknownMode(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.yaml", "version: 1\nclaude:\n  mode: tmux\n")

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load accepted an unknown mode")
	}
	if !strings.Contains(err.Error(), "tmux") {
		t.Errorf("error does not name the offending value: %v", err)
	}
}

func TestLoadRejectsUnknownGhosttyWindow(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.yaml", "version: 1\nghostty:\n  window: split\n")

	if _, err := Load(path); err == nil {
		t.Fatal("Load accepted an unknown ghostty.window")
	}
}

func TestRemoteControlDefaultsOnAndCanBeDisabled(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "none.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Claude.ShouldRemoteControl() {
		t.Error("claude.remote_control defaulted to false, want true")
	}

	dir := t.TempDir()
	path := writeFile(t, dir, "config.yaml", "version: 1\nclaude:\n  remote_control: false\n")
	off, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if off.Claude.ShouldRemoteControl() {
		t.Error("explicit remote_control: false was ignored")
	}
}

// --model on the command line outranks settings.json and ANTHROPIC_MODEL, so
// pinning it is what keeps an unattended run off a pricier model.
func TestModelDefaultsToTheRecommendedModel(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "none.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Claude.Model != DefaultModel {
		t.Errorf("claude.model = %q, want %q", cfg.Claude.Model, DefaultModel)
	}

	dir := t.TempDir()
	path := writeFile(t, dir, "config.yaml", "version: 1\nclaude:\n  model: \"\"\n")
	blank, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// An explicitly blank model is a choice, not an omission: it means "inherit
	// whatever the surrounding configuration selects".
	if blank.Claude.Model != "" {
		t.Errorf("blank model was overwritten with %q", blank.Claude.Model)
	}
}
