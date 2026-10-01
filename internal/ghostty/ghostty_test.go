package ghostty

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/YasushiKobayashi/agent-dispatch/internal/execx"
)

// recorder captures the osascript invocation instead of running it.
type recorder struct {
	name   string
	args   []string
	stdout string
	err    error
}

func (r *recorder) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.name, r.args = name, args
	return []byte(r.stdout), r.err
}

func newFor(t *testing.T, opts Options) (*Launcher, *recorder) {
	t.Helper()
	rec := &recorder{stdout: "tab-123" + idSeparator + "TERM-456\n"}
	opts.Runner = rec
	return New(opts), rec
}

func TestLaunchReturnsTabAndTerminalIDs(t *testing.T) {
	l, rec := newFor(t, Options{})

	tab, err := l.Launch(context.Background(), Request{Command: "echo hi", WorkDir: "/tmp/wt"})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if tab.TabID != "tab-123" || tab.TerminalID != "TERM-456" {
		t.Fatalf("got %+v, want tab-123/TERM-456", tab)
	}
	if rec.name != DefaultOsascript {
		t.Fatalf("ran %q, want %q", rec.name, DefaultOsascript)
	}
	if len(rec.args) != 2 || rec.args[0] != "-e" {
		t.Fatalf("args = %q, want [-e <script>]", rec.args)
	}
}

// `new tab` without an explicit `in <window>` fails with -1708 *after* having
// already created the tab and started the command, so a retry would open the
// job twice. The generated script must never rely on the implicit form.
func TestScriptAlwaysScopesNewTabToAWindow(t *testing.T) {
	l, rec := newFor(t, Options{})
	if _, err := l.Launch(context.Background(), Request{Command: "echo hi"}); err != nil {
		t.Fatalf("Launch: %v", err)
	}

	script := rec.args[1]
	for line := range strings.SplitSeq(script, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "set t to new tab") {
			continue
		}
		if !strings.Contains(line, " in window ") && !strings.Contains(line, " in w ") {
			t.Fatalf("new tab is not scoped to a window: %q", line)
		}
	}
	if !strings.Contains(script, "if (count of windows) is 0 then") {
		t.Fatal("front mode must fall back to a new window when none is open")
	}
}

func TestWindowNewSkipsTheWindowCount(t *testing.T) {
	l, rec := newFor(t, Options{Window: WindowNew})
	if _, err := l.Launch(context.Background(), Request{Command: "echo hi"}); err != nil {
		t.Fatalf("Launch: %v", err)
	}

	script := rec.args[1]
	if strings.Contains(script, "count of windows") {
		t.Fatal("window=new must not inspect existing windows")
	}
	if !strings.Contains(script, "new window with configuration cfg") {
		t.Fatalf("missing new window: %s", script)
	}
}

func TestOptionalSurfaceSettingsAreOmittedWhenUnset(t *testing.T) {
	l, rec := newFor(t, Options{})
	if _, err := l.Launch(context.Background(), Request{Command: "echo hi"}); err != nil {
		t.Fatalf("Launch: %v", err)
	}

	script := rec.args[1]
	for _, unwanted := range []string{
		"initial working directory",
		"environment variables",
		"wait after command",
		"focus trm",
	} {
		if strings.Contains(script, unwanted) {
			t.Errorf("script sets %q even though the request did not ask for it", unwanted)
		}
	}
}

func TestOptionalSurfaceSettingsAreEmittedWhenSet(t *testing.T) {
	l, rec := newFor(t, Options{Activate: true})
	_, err := l.Launch(context.Background(), Request{
		Command:  "echo hi",
		WorkDir:  "/tmp/wt",
		Env:      []string{"A=1", "B=2"},
		KeepOpen: true,
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	script := rec.args[1]
	for _, want := range []string{
		`set initial working directory of cfg to "/tmp/wt"`,
		`set environment variables of cfg to {"A=1", "B=2"}`,
		"set wait after command of cfg to true",
		"focus trm",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script is missing %q\n---\n%s", want, script)
		}
	}
}

// scripted answers lsappinfo like the real one and records every call.
type scripted struct {
	front   string
	osaErr  error
	calls   [][]string
	scripts []string
}

func (s *scripted) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	s.calls = append(s.calls, append([]string{name}, args...))
	switch {
	case name == DefaultLsappinfo && len(args) == 1 && args[0] == "visibleProcessList":
		return []byte(`ASN:0x0-0x18018-"Front_App": ASN:0x0-0x19019-"Ghostty": ` + "\n"), nil
	case name == DefaultLsappinfo:
		if got := args[len(args)-1]; got != "ASN:0x0-0x18018" {
			return nil, errors.New("lsappinfo info asked about " + got + ", not the first visible app")
		}
		return []byte(`"CFBundleIdentifier"="` + s.front + `"` + "\n"), nil
	case name == DefaultOsascript:
		s.scripts = append(s.scripts, args[len(args)-1])
		if s.osaErr != nil {
			return nil, s.osaErr
		}
	}
	return []byte("tab-1" + idSeparator + "TERM-1\n"), nil
}

// Ghostty activates itself on `new tab`, so a review detected while the user
// is typing elsewhere would steal the keyboard. Without activate, both the
// user's tab and the app they were in have to come back.
func TestLaunchWithoutActivateHandsFocusBack(t *testing.T) {
	s := &scripted{front: "com.apple.Safari"}
	l := New(Options{Runner: s})

	if _, err := l.Launch(context.Background(), Request{Command: "echo hi"}); err != nil {
		t.Fatalf("Launch: %v", err)
	}

	// One script: a second osascript for the restore would race Ghostty's
	// deferred activation and could lose it.
	if len(s.scripts) != 1 {
		t.Fatalf("osascript ran %d times, want 1", len(s.scripts))
	}
	script := s.scripts[0]
	for _, want := range []string{"set prev to selected tab of w", "select tab prev"} {
		if !strings.Contains(script, want) {
			t.Errorf("script is missing %q\n---\n%s", want, script)
		}
	}
	if strings.Contains(script, "focus trm") {
		t.Error("script focuses the new terminal even though activate is off")
	}

	// Ghostty activates and selects the new tab only after `new tab` returns,
	// so both restores must come after waiting for that.
	wait := strings.Index(script, "if frontmost and (id of selected tab of w) is (id of t) then exit repeat")
	selectPrev := strings.Index(script, "select tab prev")
	restore := strings.Index(script, `tell application id "com.apple.Safari" to activate`)
	if wait < 0 || restore < 0 {
		t.Fatalf("script does not wait for Ghostty and restore Safari:\n%s", script)
	}
	if wait >= selectPrev || selectPrev >= restore {
		t.Fatalf("wait (%d) must precede select tab prev (%d), which must precede the restore (%d):\n%s",
			wait, selectPrev, restore, script)
	}
	if !strings.HasSuffix(script, "return ids\n") {
		t.Fatalf("the ids must still be the script's result:\n%s", script)
	}
}

// A window has no previous tab to select, but Ghostty still activates late.
func TestLaunchWithoutActivateWaitsBeforeRestoringFromANewWindow(t *testing.T) {
	s := &scripted{front: "com.apple.Safari"}
	l := New(Options{Runner: s, Window: WindowNew})

	if _, err := l.Launch(context.Background(), Request{Command: "echo hi"}); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	script := s.scripts[0]
	wait := strings.Index(script, "if frontmost then exit repeat")
	restore := strings.Index(script, `tell application id "com.apple.Safari" to activate`)
	if wait < 0 || restore < 0 || wait > restore {
		t.Fatalf("script must wait for Ghostty before restoring Safari:\n%s", script)
	}
}

// When the user is already in Ghostty, re-selecting their tab is enough.
func TestLaunchWithoutActivateSkipsRestoreWhenGhosttyWasFront(t *testing.T) {
	s := &scripted{front: bundleID}
	l := New(Options{Runner: s})

	if _, err := l.Launch(context.Background(), Request{Command: "echo hi"}); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if strings.Contains(s.scripts[0], "to activate") {
		t.Fatalf("script activates an app although Ghostty was already front:\n%s", s.scripts[0])
	}
	if !strings.Contains(s.scripts[0], "select tab prev") {
		t.Fatal("the user's tab must still be selected again")
	}
}

func TestLaunchWithActivateLeavesFocusOnTheNewTab(t *testing.T) {
	s := &scripted{front: "com.apple.Safari"}
	l := New(Options{Runner: s, Activate: true})

	if _, err := l.Launch(context.Background(), Request{Command: "echo hi"}); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	for _, call := range s.calls {
		if call[0] == DefaultLsappinfo {
			t.Fatal("activate=true must not look up the frontmost app")
		}
	}
	if len(s.scripts) != 1 {
		t.Fatalf("osascript ran %d times, want 1", len(s.scripts))
	}
	for _, unwanted := range []string{"select tab prev", "repeat", "to activate"} {
		if strings.Contains(s.scripts[0], unwanted) {
			t.Fatalf("activate=true must not restore focus (%q):\n%s", unwanted, s.scripts[0])
		}
	}
}

// A tab that failed to open leaves nothing to hand focus back from.
func TestLaunchDoesNotRestoreFocusWhenTheTabFailed(t *testing.T) {
	s := &scripted{front: "com.apple.Safari", osaErr: errors.New("-1743")}
	l := New(Options{Runner: s})

	if _, err := l.Launch(context.Background(), Request{Command: "echo hi"}); err == nil {
		t.Fatal("expected an error")
	}
	if len(s.scripts) != 1 {
		t.Fatalf("osascript ran %d times, want 1", len(s.scripts))
	}
}

func TestParseBundleID(t *testing.T) {
	cases := map[string]string{
		`"CFBundleIdentifier"="com.apple.finder"` + "\n": "com.apple.finder",
		`"CFBundleIdentifier"=[ NULL ]`:                  "",
		"":                                               "",
		`"CFBundleIdentifier"=""`:                        "",
		`"CFBundleIdentifier"="a\"b"`:                    "",
	}
	for in, want := range cases {
		if got := parseBundleID(in); got != want {
			t.Errorf("parseBundleID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLaunchRejectsAnEmptyCommand(t *testing.T) {
	l, rec := newFor(t, Options{})
	if _, err := l.Launch(context.Background(), Request{WorkDir: "/tmp"}); err == nil {
		t.Fatal("expected an error for an empty command")
	}
	if rec.name != "" {
		t.Fatal("osascript must not run when the request is invalid")
	}
}

func TestLaunchWrapsOsascriptFailure(t *testing.T) {
	rec := &recorder{err: errors.New("-1743: Not authorized to send Apple events")}
	l := New(Options{Runner: rec})

	_, err := l.Launch(context.Background(), Request{Command: "echo hi"})
	if err == nil {
		t.Fatal("expected an error")
	}
	// The TCC failure is the one every launchd install hits first; it has to
	// survive into the message the user actually reads.
	if !strings.Contains(err.Error(), "Not authorized") {
		t.Fatalf("error lost the cause: %v", err)
	}
}

func TestEnsureOpensTheConfiguredApp(t *testing.T) {
	l, rec := newFor(t, Options{AppPath: "/Applications/Ghostty.app"})
	if err := l.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if rec.name != "/usr/bin/open" {
		t.Fatalf("ran %q, want /usr/bin/open", rec.name)
	}
	if strings.Join(rec.args, " ") != "-g -a /Applications/Ghostty.app" {
		t.Fatalf("args = %q", rec.args)
	}
}

func TestAppleScriptStringEscaping(t *testing.T) {
	cases := []struct{ in, want string }{
		{`plain`, `"plain"`},
		{`say "hi"`, `"say \"hi\""`},
		{`back\slash`, `"back\\slash"`},
		{"two\nlines", `"two\nlines"`},
		{"a\tb", `"a\tb"`},
	}
	for _, tc := range cases {
		if got := appleScriptString(tc.in); got != tc.want {
			t.Errorf("appleScriptString(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

// A review prompt is multi-line and full of quotes and backticks. It has to
// survive AppleScript quoting intact, so parse the literal back out rather than
// counting characters.
func TestPromptSurvivesAppleScriptQuoting(t *testing.T) {
	prompt := "レビューして\n`git diff origin/main...HEAD`\nsaid \"no\" \\ done"

	literal := appleScriptString(prompt)
	if strings.Contains(literal[1:len(literal)-1], "\n") {
		t.Fatal("a raw newline inside the literal would end the AppleScript statement")
	}

	got := unquoteAppleScript(t, literal)
	if got != prompt {
		t.Fatalf("round trip changed the prompt:\n got %q\nwant %q", got, prompt)
	}
}

// unquoteAppleScript reverses appleScriptString the way AppleScript would.
func unquoteAppleScript(t *testing.T, literal string) string {
	t.Helper()
	if len(literal) < 2 || literal[0] != '"' || literal[len(literal)-1] != '"' {
		t.Fatalf("not a quoted literal: %s", literal)
	}
	body := literal[1 : len(literal)-1]

	var sb strings.Builder
	for i := 0; i < len(body); i++ {
		if body[i] != '\\' || i+1 >= len(body) {
			sb.WriteByte(body[i])
			continue
		}
		i++
		switch body[i] {
		case 'n':
			sb.WriteByte('\n')
		case 'r':
			sb.WriteByte('\r')
		case 't':
			sb.WriteByte('\t')
		default:
			sb.WriteByte(body[i])
		}
	}
	return sb.String()
}

func TestShellQuoteHandlesEmbeddedSingleQuotes(t *testing.T) {
	if got, want := ShellQuote(`it's`), `'it'\''s'`; got != want {
		t.Fatalf("ShellQuote = %s, want %s", got, want)
	}
}

// The quoting has to hold against a real shell, not just against our
// expectations of one.
func TestShellQuoteRoundTripsThroughARealShell(t *testing.T) {
	for _, raw := range []string{
		`plain`,
		`it's`,
		`a "b" c`,
		"back`tick`",
		`$(echo pwned)`,
		"multi\nline",
		`semi; colon && and`,
	} {
		out, err := exec.Command("/bin/sh", "-c", "printf %s "+ShellQuote(raw)).Output()
		if err != nil {
			t.Fatalf("sh failed for %q: %v", raw, err)
		}
		if string(out) != raw {
			t.Errorf("round trip changed %q into %q", raw, string(out))
		}
	}
}

// Sh is what actually reaches Ghostty, which parses it with a shell of its own
// before the inner sh sees the script. Both layers have to survive.
func TestShSurvivesTwoLayersOfShellParsing(t *testing.T) {
	script := `exec ` + ShellQuote("/bin/echo") + ` ` + ShellQuote(`a "b" $(nope) 'c'`)
	command := Sh(script)

	// Layer 1: Ghostty's shell splits `command` into argv.
	argv, err := exec.Command("/bin/sh", "-c",
		`eval 'for a in '`+ShellQuote(command)+`'; do printf "%s\n" "$a"; done'`).Output()
	if err != nil {
		t.Fatalf("layer 1 failed: %v", err)
	}
	fields := strings.Split(strings.TrimRight(string(argv), "\n"), "\n")
	if len(fields) != 3 || fields[0] != "/bin/sh" || fields[1] != "-c" {
		t.Fatalf("layer 1 argv = %q, want [/bin/sh -c <script>]", fields)
	}

	// Layer 2: that inner sh runs the script.
	out, err := exec.Command("/bin/sh", "-c", fields[2]).Output()
	if err != nil {
		t.Fatalf("layer 2 failed: %v", err)
	}
	if got, want := strings.TrimRight(string(out), "\n"), `a "b" $(nope) 'c'`; got != want {
		t.Fatalf("argument arrived as %q, want %q", got, want)
	}
}

// New must not leave a nil Runner behind for a caller that supplied none.
// `tab` inside a `tell application "Ghostty"` block is Ghostty's tab class,
// not AppleScript's tab character: using it welds both ids into one string.
func TestScriptDoesNotUseTheTabTerminology(t *testing.T) {
	l, rec := newFor(t, Options{})
	if _, err := l.Launch(context.Background(), Request{Command: "echo hi"}); err != nil {
		t.Fatalf("Launch: %v", err)
	}

	script := rec.args[1]
	if strings.Contains(script, "& tab &") {
		t.Fatalf("script uses the shadowed `tab` constant:\n%s", script)
	}
	if !strings.Contains(script, `& "`+idSeparator+`" &`) {
		t.Fatalf("script does not join the ids with a literal separator:\n%s", script)
	}
}

func TestNewFillsInDefaults(t *testing.T) {
	l := New(Options{})
	if l.opts.Osascript != DefaultOsascript || l.opts.AppPath != DefaultAppPath {
		t.Fatalf("defaults not applied: %+v", l.opts)
	}
	if l.opts.Window != WindowFront {
		t.Fatalf("window = %q, want %q", l.opts.Window, WindowFront)
	}
	if _, ok := l.opts.Runner.(execx.Exec); !ok {
		t.Fatalf("runner = %T, want execx.Exec", l.opts.Runner)
	}
}
