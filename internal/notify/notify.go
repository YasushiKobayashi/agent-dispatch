// Package notify raises a macOS notification when a run finishes.
//
// Notifications are best effort: reviewd runs unattended, and a missing
// notification utility must never turn a completed review into a failure.
package notify

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/YasushiKobayashi/agent-dispatch/internal/execx"
)

// Notification is what the user sees.
type Notification struct {
	Title    string
	Subtitle string
	Message  string
	// OpenPath, when set and terminal-notifier is available, is opened when the
	// notification is clicked. osascript cannot do this, so it is dropped in
	// the fallback path.
	OpenPath string
}

// Notifier delivers notifications through terminal-notifier when it is
// installed, and osascript otherwise.
type Notifier struct {
	enabled       bool
	preferTermNot bool
	runner        execx.Runner
	lookPath      func(string) (string, error)
}

// Options configures a Notifier.
type Options struct {
	Enabled                bool
	PreferTerminalNotifier bool
	Runner                 execx.Runner
	// LookPath is injectable for tests; defaults to exec.LookPath.
	LookPath func(string) (string, error)
}

// New returns a Notifier.
func New(opts Options) *Notifier {
	if opts.Runner == nil {
		opts.Runner = execx.Exec{}
	}
	if opts.LookPath == nil {
		opts.LookPath = exec.LookPath
	}
	return &Notifier{
		enabled:       opts.Enabled,
		preferTermNot: opts.PreferTerminalNotifier,
		runner:        opts.Runner,
		lookPath:      opts.LookPath,
	}
}

// Notify delivers n. It returns an error only so callers can log it; a failure
// here is never fatal.
func (n *Notifier) Notify(ctx context.Context, note Notification) error {
	if n == nil || !n.enabled {
		return nil
	}
	note.Title = oneLine(note.Title, 80)
	note.Subtitle = oneLine(note.Subtitle, 80)
	note.Message = oneLine(note.Message, 200)
	if note.Title == "" {
		note.Title = "reviewd"
	}

	if n.preferTermNot {
		if path, err := n.lookPath("terminal-notifier"); err == nil {
			args := []string{"-title", note.Title, "-message", note.Message}
			if note.Subtitle != "" {
				args = append(args, "-subtitle", note.Subtitle)
			}
			if note.OpenPath != "" {
				args = append(args, "-open", "file://"+note.OpenPath)
			}
			if _, err := n.runner.Run(ctx, path, args...); err != nil {
				return fmt.Errorf("terminal-notifier: %w", err)
			}
			return nil
		}
	}

	script := "display notification " + appleScriptString(note.Message) +
		" with title " + appleScriptString(note.Title)
	if note.Subtitle != "" {
		script += " subtitle " + appleScriptString(note.Subtitle)
	}
	if _, err := n.runner.Run(ctx, "osascript", "-e", script); err != nil {
		return fmt.Errorf("osascript: %w", err)
	}
	return nil
}

// appleScriptString renders s as an AppleScript string literal.
//
// AppleScript literals understand only \" and \\, and cannot contain a raw
// newline, so those are handled explicitly rather than passed through.
func appleScriptString(s string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"\n", " ",
		"\r", " ",
		"\t", " ",
	)
	return `"` + replacer.Replace(s) + `"`
}

// oneLine collapses s to a single line and caps its length.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}
