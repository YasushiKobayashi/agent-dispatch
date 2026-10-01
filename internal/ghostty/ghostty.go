// Package ghostty opens a Ghostty terminal tab running an interactive command.
//
// It exists so reviewd can hand a review to a tab the user can take over,
// instead of running Claude Code headlessly and writing the answer to a file.
//
// Everything goes through Ghostty's AppleScript dictionary (Ghostty 1.3+), not
// GUI scripting, so no Accessibility permission is involved. The calling
// process does need Automation permission for Ghostty; the first attempt from a
// new parent process raises the usual macOS consent prompt, and a launchd agent
// that has never been granted it fails with -1743 instead of prompting.
package ghostty

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/YasushiKobayashi/agent-dispatch/internal/execx"
)

// Default locations. Both are overridable so tests never touch the real app.
const (
	DefaultAppPath   = "/Applications/Ghostty.app"
	DefaultOsascript = "/usr/bin/osascript"
	DefaultLsappinfo = "/usr/bin/lsappinfo"
)

// bundleID is Ghostty's own bundle identifier.
const bundleID = "com.mitchellh.ghostty"

// idSeparator joins the two ids the script returns. Any literal would do as
// long as it cannot occur inside an id and is not AppleScript terminology.
const idSeparator = "|"

// Window placement for a new tab.
const (
	// WindowFront adds the tab to Ghostty's frontmost window, falling back to a
	// new window when none is open.
	WindowFront = "front"
	// WindowNew always opens a separate window.
	WindowNew = "new"
)

// Options configures a Launcher.
type Options struct {
	// AppPath is only used to `open` Ghostty when it is not running yet.
	AppPath string
	// Osascript is the osascript binary. launchd gives us a bare PATH, so this
	// is an absolute path by default rather than a bare command name.
	Osascript string
	// Lsappinfo is the lsappinfo binary, used to remember the frontmost app.
	Lsappinfo string
	// Window is WindowFront or WindowNew.
	Window string
	// Activate brings Ghostty to the foreground after the tab is created. When
	// false, the previously selected tab and the previously frontmost app are
	// restored, because Ghostty activates itself on `new tab` / `new window`.
	// Ghostty still shows up for a moment: nothing in its scripting dictionary
	// creates a tab without activating the app.
	Activate bool
	// Runner executes osascript. Tests substitute it.
	Runner execx.Runner
}

// Launcher opens Ghostty tabs.
type Launcher struct {
	opts Options
}

// New returns a Launcher, filling in the defaults for any zero field.
func New(opts Options) *Launcher {
	if strings.TrimSpace(opts.AppPath) == "" {
		opts.AppPath = DefaultAppPath
	}
	if strings.TrimSpace(opts.Osascript) == "" {
		opts.Osascript = DefaultOsascript
	}
	if strings.TrimSpace(opts.Lsappinfo) == "" {
		opts.Lsappinfo = DefaultLsappinfo
	}
	if strings.TrimSpace(opts.Window) == "" {
		opts.Window = WindowFront
	}
	if opts.Runner == nil {
		opts.Runner = execx.Exec{}
	}
	return &Launcher{opts: opts}
}

// Request describes the tab to open.
type Request struct {
	// Command is the command line the tab runs, already shell quoted. Build it
	// with Sh and ShellQuote.
	Command string
	// WorkDir is the tab's initial working directory.
	WorkDir string
	// Env are KEY=VALUE strings exported into the tab's process.
	Env []string
	// KeepOpen leaves the tab open after the command exits, so a command that
	// fails immediately still leaves its error on screen.
	KeepOpen bool
}

// Tab identifies the tab that was opened.
type Tab struct {
	TabID      string
	TerminalID string
}

// Launch opens the tab and returns its identifiers.
func (l *Launcher) Launch(ctx context.Context, req Request) (Tab, error) {
	if strings.TrimSpace(req.Command) == "" {
		return Tab{}, fmt.Errorf("ghostty: command is required")
	}

	var front string
	if !l.opts.Activate {
		front = l.frontApp(ctx)
	}

	script := l.script(req, front)
	out, err := l.opts.Runner.Run(ctx, l.opts.Osascript, "-e", script)
	if err != nil {
		return Tab{}, fmt.Errorf("ghostty: open tab: %w", err)
	}

	// The script returns "<tab id>|<terminal id>".
	fields := strings.Split(strings.TrimSpace(string(out)), idSeparator)
	tab := Tab{TabID: strings.TrimSpace(fields[0])}
	if len(fields) > 1 {
		tab.TerminalID = strings.TrimSpace(fields[1])
	}
	return tab, nil
}

// frontApp returns the bundle id of the frontmost regular app, or "" when it
// cannot be determined. lsappinfo needs no Automation or Accessibility
// permission, unlike asking System Events.
//
// Not `lsappinfo front`: that can name a UI element such as Universal Control,
// and activating one of those leaves Ghostty in front. visibleProcessList only
// holds regular apps, frontmost first.
func (l *Launcher) frontApp(ctx context.Context) string {
	list, err := l.opts.Runner.Run(ctx, l.opts.Lsappinfo, "visibleProcessList")
	if err != nil {
		return ""
	}
	asn := asnPattern.FindString(string(list))
	if asn == "" {
		return ""
	}
	out, err := l.opts.Runner.Run(ctx, l.opts.Lsappinfo, "info", "-only", "bundleid", asn)
	if err != nil {
		return ""
	}
	return parseBundleID(string(out))
}

// asnPattern matches one entry of `lsappinfo visibleProcessList`, which looks
// like `ASN:0x0-0x18018-"Zed": ASN:0x0-0x19019-"Ghostty":`.
var asnPattern = regexp.MustCompile(`ASN:0x[0-9a-fA-F]+-0x[0-9a-fA-F]+`)

// parseBundleID extracts the value from lsappinfo's
// `"CFBundleIdentifier"="com.apple.finder"` line.
func parseBundleID(out string) string {
	_, value, ok := strings.Cut(strings.TrimSpace(out), `"CFBundleIdentifier"=`)
	// An app without a bundle id is reported as an unquoted [ NULL ].
	if !ok || len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return ""
	}
	value = value[1 : len(value)-1]
	if value == "" || strings.ContainsAny(value, "\"\\\n") {
		return ""
	}
	return value
}

// activationTimeout bounds each wait for Ghostty's own activation, in
// iterations of a 10ms delay.
const activationTimeout = 100

// script renders the AppleScript that creates the tab. front is the bundle id
// of the app to hand the foreground back to, or "" to leave it with Ghostty.
//
// `new tab` is always given an explicit `in <window>`: without it Ghostty
// answers -1708 (Can't continue new tab) *after* having already created the
// tab and started the command, so a naive retry opens the job twice.
//
// Ghostty's `new tab` / `new window` return before the app activates: the
// makeKeyAndOrderFront and NSApp.activate calls are dispatched to the main
// queue. Restoring anything before they run is undone by them, so the script
// waits for them first, and restores the front app from within the same
// script instead of a second osascript that could lose the same race.
func (l *Launcher) script(req Request, front string) string {
	var sb strings.Builder

	sb.WriteString("tell application \"Ghostty\"\n")
	sb.WriteString("  set cfg to new surface configuration\n")
	fmt.Fprintf(&sb, "  set command of cfg to %s\n", appleScriptString(req.Command))
	if strings.TrimSpace(req.WorkDir) != "" {
		fmt.Fprintf(&sb, "  set initial working directory of cfg to %s\n", appleScriptString(req.WorkDir))
	}
	if len(req.Env) > 0 {
		fmt.Fprintf(&sb, "  set environment variables of cfg to %s\n", appleScriptList(req.Env))
	}
	if req.KeepOpen {
		sb.WriteString("  set wait after command of cfg to true\n")
	}

	newWindow := func(indent string) {
		sb.WriteString(indent + "set w to new window with configuration cfg\n")
		sb.WriteString(indent + "set t to selected tab of w\n")
		if !l.opts.Activate {
			waitFor(&sb, indent, "frontmost")
		}
	}

	if l.opts.Window == WindowNew {
		newWindow("  ")
	} else {
		// A Ghostty that was launched by `open` a moment ago can briefly report
		// zero windows, and `new tab` needs one to attach to.
		sb.WriteString("  if (count of windows) is 0 then\n")
		newWindow("    ")
		sb.WriteString("  else\n")
		if l.opts.Activate {
			sb.WriteString("    set t to new tab in window 1 with configuration cfg\n")
		} else {
			// `new tab` selects the tab it creates; put the user's tab back
			// once Ghostty has done so, or the late selection wins.
			sb.WriteString("    set w to window 1\n")
			sb.WriteString("    set prev to selected tab of w\n")
			sb.WriteString("    set t to new tab in w with configuration cfg\n")
			waitFor(&sb, "    ", "frontmost and (id of selected tab of w) is (id of t)")
			sb.WriteString("    select tab prev\n")
		}
		sb.WriteString("  end if\n")
	}

	sb.WriteString("  set trm to focused terminal of t\n")
	if l.opts.Activate {
		sb.WriteString("  focus trm\n")
	}
	// Not AppleScript's `tab` constant: inside `tell application "Ghostty"`
	// that name resolves to Ghostty's own `tab` class and concatenates the
	// literal text "tab", which silently welds both ids into one string.
	fmt.Fprintf(&sb, "  set ids to (id of t) & %s & (id of trm)\n", appleScriptString(idSeparator))
	sb.WriteString("end tell\n")

	if front != "" && front != bundleID {
		// Best effort: the tab is already open, so a failure here must not
		// turn the launch into an error (and a retry into a duplicate job).
		sb.WriteString("try\n")
		fmt.Fprintf(&sb, "  tell application id %s to activate\n", appleScriptString(front))
		sb.WriteString("end try\n")
	}
	sb.WriteString("return ids\n")

	return sb.String()
}

// waitFor polls an AppleScript condition until it holds, giving up after
// activationTimeout iterations so a Ghostty that never activates cannot hang
// the launch.
func waitFor(sb *strings.Builder, indent, condition string) {
	fmt.Fprintf(sb, "%srepeat %d times\n", indent, activationTimeout)
	fmt.Fprintf(sb, "%s  if %s then exit repeat\n", indent, condition)
	fmt.Fprintf(sb, "%s  delay 0.01\n", indent)
	fmt.Fprintf(sb, "%send repeat\n", indent)
}

// Ensure starts Ghostty if it is not already running. Launch works either way,
// but starting the app first keeps the AppleScript from racing a cold start.
func (l *Launcher) Ensure(ctx context.Context) error {
	if _, err := l.opts.Runner.Run(ctx, "/usr/bin/open", "-g", "-a", l.opts.AppPath); err != nil {
		return fmt.Errorf("ghostty: launch %s: %w", l.opts.AppPath, err)
	}
	return nil
}

// Sh wraps a shell script so Ghostty runs it under /bin/sh. Ghostty hands
// `command` to a shell of its own, so the script has to survive one round of
// shell parsing before the inner sh ever sees it.
func Sh(script string) string { return "/bin/sh -c " + ShellQuote(script) }

// ShellQuote wraps s in single quotes, which are literal in POSIX sh except
// for the closing quote itself.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// appleScriptString renders s as an AppleScript string literal. Backslash and
// double quote need escaping, and a literal newline would end the statement.
func appleScriptString(s string) string {
	r := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"\n", `\n`,
		"\r", `\r`,
		"\t", `\t`,
	)
	return `"` + r.Replace(s) + `"`
}

// appleScriptList renders items as an AppleScript list of strings.
func appleScriptList(items []string) string {
	quoted := make([]string, 0, len(items))
	for _, item := range items {
		quoted = append(quoted, appleScriptString(item))
	}
	return "{" + strings.Join(quoted, ", ") + "}"
}
