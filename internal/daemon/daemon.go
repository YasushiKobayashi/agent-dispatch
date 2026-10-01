// Package daemon manages reviewd's launchd agent.
package daemon

import (
	"encoding/xml"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/YasushiKobayashi/agent-dispatch/internal/execx"
)

// Label is the launchd job label, and also the plist's file name stem.
const Label = "com.yasushikobayashi.reviewd"

// Options describes the agent to install.
type Options struct {
	// BinaryPath is the absolute path to the reviewd executable.
	BinaryPath string
	// ConfigPath and RulesPath are passed through as flags so the agent does
	// not depend on the user's config directory resolving the same way.
	ConfigPath string
	RulesPath  string
	// LogsDir receives launchd's stdout and stderr.
	LogsDir string
	// Interval is how often launchd starts a poll, in seconds.
	Interval int
	// PathEnv is the PATH the agent runs with.
	PathEnv string
	// Env are extra environment variables baked into the plist, on top of PATH
	// and HOME. See PassthroughEnv.
	Env map[string]string
}

// PassthroughEnv names the environment variables `daemon install` copies from
// the installing shell into the plist. launchd starts an agent with a bare
// environment, so without these the agent cannot resolve gh or claude -- see
// execx.ToolEnv for what each one is for.
var PassthroughEnv = execx.ToolEnv

// CollectEnv reads PassthroughEnv from the current process, skipping anything
// unset or empty.
func CollectEnv() map[string]string { return execx.CollectToolEnv() }

// PlistPath is where the agent's plist lives.
func PlistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", Label+".plist"), nil
}

// DefaultPath builds the PATH to bake into the plist.
//
// launchd starts agents with a minimal PATH that does not include Homebrew,
// aqua, mise or asdf shims, so `gh`, `git` and `claude` would not resolve. The
// directories of the tools we actually found are prepended to a sane baseline.
func DefaultPath(tools ...string) string {
	var dirs []string
	seen := map[string]bool{}
	add := func(dir string) {
		if dir == "" || seen[dir] {
			return
		}
		seen[dir] = true
		dirs = append(dirs, dir)
	}

	for _, tool := range tools {
		if tool == "" {
			continue
		}
		if filepath.IsAbs(tool) {
			add(filepath.Dir(tool))
			continue
		}
		if resolved, err := exec.LookPath(tool); err == nil {
			add(filepath.Dir(resolved))
		}
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		add(dir)
	}
	for _, dir := range []string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"} {
		add(dir)
	}
	return strings.Join(dirs, ":")
}

// Plist renders the launchd property list for the agent.
func Plist(opts Options) (string, error) {
	if opts.Interval <= 0 {
		opts.Interval = 180
	}
	if !filepath.IsAbs(opts.BinaryPath) {
		return "", fmt.Errorf("binary path must be absolute, got %q", opts.BinaryPath)
	}

	args := []string{opts.BinaryPath}
	if opts.ConfigPath != "" {
		args = append(args, "--config", opts.ConfigPath)
	}
	if opts.RulesPath != "" {
		args = append(args, "--rules", opts.RulesPath)
	}
	args = append(args, "poll")

	var sb strings.Builder
	sb.WriteString(xml.Header)
	sb.WriteString("<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n")
	sb.WriteString("<plist version=\"1.0\">\n<dict>\n")
	writeKeyString(&sb, "Label", Label)

	sb.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	for _, arg := range args {
		fmt.Fprintf(&sb, "    <string>%s</string>\n", escape(arg))
	}
	sb.WriteString("  </array>\n")

	fmt.Fprintf(&sb, "  <key>StartInterval</key>\n  <integer>%d</integer>\n", opts.Interval)
	sb.WriteString("  <key>RunAtLoad</key>\n  <true/>\n")
	// The poll is short-lived by design; letting launchd restart it on exit
	// would spin.
	sb.WriteString("  <key>KeepAlive</key>\n  <false/>\n")
	writeKeyString(&sb, "StandardOutPath", filepath.Join(opts.LogsDir, "reviewd.out.log"))
	writeKeyString(&sb, "StandardErrorPath", filepath.Join(opts.LogsDir, "reviewd.err.log"))
	writeKeyString(&sb, "WorkingDirectory", filepath.Dir(opts.BinaryPath))

	sb.WriteString("  <key>EnvironmentVariables</key>\n  <dict>\n")
	fmt.Fprintf(&sb, "    <key>PATH</key>\n    <string>%s</string>\n", escape(opts.PathEnv))
	if home, err := os.UserHomeDir(); err == nil {
		fmt.Fprintf(&sb, "    <key>HOME</key>\n    <string>%s</string>\n", escape(home))
	}
	// Sorted so regenerating the plist without changing anything produces an
	// identical file.
	for _, name := range slices.Sorted(maps.Keys(opts.Env)) {
		if name == "PATH" || name == "HOME" {
			continue
		}
		fmt.Fprintf(&sb, "    <key>%s</key>\n    <string>%s</string>\n", escape(name), escape(opts.Env[name]))
	}
	sb.WriteString("  </dict>\n")

	sb.WriteString("</dict>\n</plist>\n")
	return sb.String(), nil
}

func writeKeyString(sb *strings.Builder, key, value string) {
	fmt.Fprintf(sb, "  <key>%s</key>\n  <string>%s</string>\n", escape(key), escape(value))
}

func escape(s string) string {
	var buf strings.Builder
	if err := xml.EscapeText(&buf, []byte(s)); err != nil {
		return s
	}
	return buf.String()
}

// Install writes the plist and loads the agent, replacing any previous one.
func Install(opts Options) (string, error) {
	body, err := Plist(opts)
	if err != nil {
		return "", err
	}
	path, err := PlistPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if opts.LogsDir != "" {
		if err := os.MkdirAll(opts.LogsDir, 0o755); err != nil {
			return "", fmt.Errorf("create %s: %w", opts.LogsDir, err)
		}
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}

	// bootout first so a re-install picks up the new plist; it fails when the
	// agent was not loaded, which is fine.
	_ = run("launchctl", "bootout", domain()+"/"+Label)
	if err := run("launchctl", "bootstrap", domain(), path); err != nil {
		return path, fmt.Errorf("load the agent: %w", err)
	}
	return path, nil
}

// Uninstall unloads the agent and removes its plist.
func Uninstall() (string, error) {
	path, err := PlistPath()
	if err != nil {
		return "", err
	}
	_ = run("launchctl", "bootout", domain()+"/"+Label)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return path, fmt.Errorf("remove %s: %w", path, err)
	}
	return path, nil
}

// Status reports launchctl's view of the agent.
func Status() (string, error) {
	out, err := exec.Command("launchctl", "print", domain()+"/"+Label).CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out)), fmt.Errorf("agent is not loaded")
	}
	return strings.TrimSpace(string(out)), nil
}

func domain() string { return fmt.Sprintf("gui/%d", os.Getuid()) }

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), msg)
	}
	return nil
}
