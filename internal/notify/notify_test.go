package notify

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/YasushiKobayashi/agent-dispatch/internal/execx"
)

type capture struct {
	calls [][]string
	err   error
}

func (c *capture) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	c.calls = append(c.calls, append([]string{name}, args...))
	return nil, c.err
}

func (c *capture) last() string {
	if len(c.calls) == 0 {
		return ""
	}
	return strings.Join(c.calls[len(c.calls)-1], "\x00")
}

func found(string) (string, error)   { return "/opt/homebrew/bin/terminal-notifier", nil }
func missing(string) (string, error) { return "", exec.ErrNotFound }

func note() Notification {
	return Notification{
		Title:    "reviewd",
		Subtitle: "acme/widgets#42",
		Message:  "指摘 3 件",
		OpenPath: "/Users/me/.reviewd/runs/x/result.md",
	}
}

func TestNotifyDisabledDoesNothing(t *testing.T) {
	c := &capture{}
	n := New(Options{Enabled: false, Runner: c, LookPath: found})

	if err := n.Notify(context.Background(), note()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if len(c.calls) != 0 {
		t.Errorf("ran %v while disabled", c.calls)
	}
}

func TestNotifyUsesTerminalNotifierWhenAvailable(t *testing.T) {
	c := &capture{}
	n := New(Options{Enabled: true, PreferTerminalNotifier: true, Runner: c, LookPath: found})

	if err := n.Notify(context.Background(), note()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	got := c.last()
	for _, want := range []string{
		"/opt/homebrew/bin/terminal-notifier", "-title\x00reviewd",
		"-message\x00指摘 3 件", "-subtitle\x00acme/widgets#42",
		"-open\x00file:///Users/me/.reviewd/runs/x/result.md",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("call %q missing %q", got, want)
		}
	}
}

func TestNotifyFallsBackToOsascript(t *testing.T) {
	c := &capture{}
	n := New(Options{Enabled: true, PreferTerminalNotifier: true, Runner: c, LookPath: missing})

	if err := n.Notify(context.Background(), note()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	call := c.calls[0]
	if call[0] != "osascript" || call[1] != "-e" {
		t.Fatalf("call = %v, want osascript -e ...", call)
	}
	script := call[2]
	for _, want := range []string{
		`display notification "指摘 3 件"`,
		`with title "reviewd"`,
		`subtitle "acme/widgets#42"`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script %q missing %q", script, want)
		}
	}
}

func TestNotifySkipsTerminalNotifierWhenNotPreferred(t *testing.T) {
	c := &capture{}
	n := New(Options{Enabled: true, PreferTerminalNotifier: false, Runner: c, LookPath: found})

	if err := n.Notify(context.Background(), note()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if c.calls[0][0] != "osascript" {
		t.Errorf("used %q, want osascript when terminal-notifier is not preferred", c.calls[0][0])
	}
}

func TestNotifyReportsRunnerFailure(t *testing.T) {
	c := &capture{err: errors.New("no such process")}
	n := New(Options{Enabled: true, Runner: c, LookPath: missing})

	if err := n.Notify(context.Background(), note()); err == nil {
		t.Fatal("Notify swallowed a runner failure; the caller needs it for the log")
	}
}

func TestAppleScriptEscaping(t *testing.T) {
	cases := map[string]string{
		`plain`:            `"plain"`,
		`say "hi"`:         `"say \"hi\""`,
		`back\slash`:       `"back\\slash"`,
		"two\nlines":       `"two lines"`,
		`both "\" at once`: `"both \"\\\" at once"`,
	}
	for in, want := range cases {
		if got := appleScriptString(in); got != want {
			t.Errorf("appleScriptString(%q) = %s, want %s", in, got, want)
		}
	}
}

// A multi-line review body must not be able to break out of the AppleScript
// literal, whatever it contains.
func TestNotifyNeverBreaksOutOfTheAppleScriptLiteral(t *testing.T) {
	c := &capture{}
	n := New(Options{Enabled: true, Runner: c, LookPath: missing})

	message := `line1` + "\n" + `line2 " end\ and " again`
	if err := n.Notify(context.Background(), Notification{Title: "reviewd", Message: message}); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	script := c.calls[0][2]
	if strings.ContainsAny(script, "\n\r") {
		t.Fatalf("script contains a raw newline: %q", script)
	}

	literals := appleScriptLiterals(t, script)
	if len(literals) != 2 {
		t.Fatalf("parsed %d literals from %q, want 2", len(literals), script)
	}
	if want := oneLine(message, 200); literals[0] != want {
		t.Errorf("message literal = %q, want %q", literals[0], want)
	}
	if literals[1] != "reviewd" {
		t.Errorf("title literal = %q, want %q", literals[1], "reviewd")
	}
}

// appleScriptLiterals extracts the string literals from an AppleScript
// fragment the way AppleScript itself would, so the test proves the quoting is
// well formed instead of counting characters.
func appleScriptLiterals(t *testing.T, script string) []string {
	t.Helper()

	var (
		out     []string
		sb      strings.Builder
		inside  bool
		escaped bool
	)
	for _, r := range script {
		switch {
		case escaped:
			sb.WriteRune(r)
			escaped = false
		case inside && r == '\\':
			escaped = true
		case r == '"':
			if inside {
				out = append(out, sb.String())
				sb.Reset()
			}
			inside = !inside
		case inside:
			sb.WriteRune(r)
		}
	}
	if inside {
		t.Fatalf("unterminated string literal in %q", script)
	}
	if escaped {
		t.Fatalf("trailing escape in %q", script)
	}
	return out
}

func TestOneLineCollapsesAndTruncates(t *testing.T) {
	if got := oneLine("  a\n b\tc  ", 100); got != "a b c" {
		t.Errorf("oneLine = %q, want %q", got, "a b c")
	}
	if got := oneLine("あいうえお", 3); got != "あいう…" {
		t.Errorf("oneLine = %q, want %q", got, "あいう…")
	}
}

func TestNotifyDefaultsTitle(t *testing.T) {
	c := &capture{}
	n := New(Options{Enabled: true, Runner: c, LookPath: missing})

	n.Notify(context.Background(), Notification{Message: "done"})
	if !strings.Contains(c.calls[0][2], `with title "reviewd"`) {
		t.Errorf("script = %q, want a default title", c.calls[0][2])
	}
}

var _ execx.Runner = (*capture)(nil)
