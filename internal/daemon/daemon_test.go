package daemon

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func opts() Options {
	return Options{
		BinaryPath: "/Users/me/.local/bin/reviewd",
		ConfigPath: "/Users/me/.config/reviewd/config.yaml",
		RulesPath:  "/Users/me/.config/reviewd/rules.yaml",
		LogsDir:    "/Users/me/.reviewd/logs",
		Interval:   180,
		PathEnv:    "/opt/homebrew/bin:/usr/bin:/bin",
	}
}

func TestPlistIsWellFormedXML(t *testing.T) {
	body, err := Plist(opts())
	if err != nil {
		t.Fatalf("Plist: %v", err)
	}

	decoder := xml.NewDecoder(strings.NewReader(body))
	decoder.Strict = false
	for {
		_, err := decoder.Token()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			t.Fatalf("plist is not well-formed XML: %v\n%s", err, body)
		}
	}
}

func TestPlistContents(t *testing.T) {
	body, err := Plist(opts())
	if err != nil {
		t.Fatalf("Plist: %v", err)
	}

	for _, want := range []string{
		"<string>" + Label + "</string>",
		"<string>/Users/me/.local/bin/reviewd</string>",
		"<string>--config</string>",
		"<string>/Users/me/.config/reviewd/config.yaml</string>",
		"<string>--rules</string>",
		"<string>poll</string>",
		"<integer>180</integer>",
		"<key>RunAtLoad</key>\n  <true/>",
		"<string>/Users/me/.reviewd/logs/reviewd.out.log</string>",
		"<string>/Users/me/.reviewd/logs/reviewd.err.log</string>",
		// launchd's default PATH omits Homebrew/aqua/mise, so it must be pinned.
		"<key>PATH</key>\n    <string>/opt/homebrew/bin:/usr/bin:/bin</string>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("plist is missing %q\n---\n%s", want, body)
		}
	}

	// A short-lived poll must not be restarted the instant it exits.
	if !strings.Contains(body, "<key>KeepAlive</key>\n  <false/>") {
		t.Errorf("plist should set KeepAlive to false\n%s", body)
	}

	// `poll` must come last so the flags apply to it.
	if idx := strings.LastIndex(body, "<string>poll</string>"); idx < strings.Index(body, "--rules") {
		t.Error("poll is not the final ProgramArgument")
	}
}

func TestPlistDefaultsInterval(t *testing.T) {
	o := opts()
	o.Interval = 0

	body, err := Plist(o)
	if err != nil {
		t.Fatalf("Plist: %v", err)
	}
	if !strings.Contains(body, "<integer>180</integer>") {
		t.Error("plist did not default StartInterval to 180")
	}
}

func TestPlistRejectsRelativeBinary(t *testing.T) {
	o := opts()
	o.BinaryPath = "bin/reviewd"

	if _, err := Plist(o); err == nil {
		t.Fatal("Plist accepted a relative binary path; launchd would not find it")
	}
}

func TestPlistEscapesXML(t *testing.T) {
	o := opts()
	o.ConfigPath = `/tmp/a & b/<config>.yaml`

	body, err := Plist(o)
	if err != nil {
		t.Fatalf("Plist: %v", err)
	}
	if strings.Contains(body, "a & b") || strings.Contains(body, "<config>.yaml") {
		t.Errorf("path was not XML-escaped:\n%s", body)
	}
	if !strings.Contains(body, "a &amp; b") {
		t.Errorf("expected the escaped form in:\n%s", body)
	}
}

func TestDefaultPathPutsToolDirectoriesFirst(t *testing.T) {
	dir := t.TempDir()
	tool := filepath.Join(dir, "gh")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := DefaultPath(tool, "claude")
	if !strings.HasPrefix(got, dir+":") {
		t.Errorf("PATH = %q, want it to start with the tool's directory %q", got, dir)
	}
	for _, want := range []string{"/usr/bin", "/bin"} {
		if !strings.Contains(got, want) {
			t.Errorf("PATH = %q, want it to include %q", got, want)
		}
	}
}

func TestDefaultPathHasNoDuplicates(t *testing.T) {
	seen := map[string]bool{}
	for _, dir := range strings.Split(DefaultPath("git", "sh"), ":") {
		if seen[dir] {
			t.Errorf("duplicate PATH entry %q", dir)
		}
		seen[dir] = true
	}
}

func TestPlistPath(t *testing.T) {
	path, err := PlistPath()
	if err != nil {
		t.Fatalf("PlistPath: %v", err)
	}
	if filepath.Base(path) != Label+".plist" {
		t.Errorf("PlistPath = %q, want it to end with %s.plist", path, Label)
	}
	if !strings.Contains(path, filepath.Join("Library", "LaunchAgents")) {
		t.Errorf("PlistPath = %q, want it under ~/Library/LaunchAgents", path)
	}
}

// PATH alone is not enough when gh and claude are aqua shims: the shim resolves
// the real binary through AQUA_GLOBAL_CONFIG, and launchd starts with a bare
// environment, so without it aqua answers "command is not found" for a binary
// that is plainly on PATH.
func TestPlistCarriesPassthroughEnv(t *testing.T) {
	plist, err := Plist(Options{
		BinaryPath: "/usr/local/bin/reviewd",
		LogsDir:    "/tmp/logs",
		Interval:   180,
		PathEnv:    "/usr/bin",
		Env:        map[string]string{"AQUA_GLOBAL_CONFIG": "/home/me/aqua.yaml"},
	})
	if err != nil {
		t.Fatalf("Plist: %v", err)
	}
	if !strings.Contains(plist, "<key>AQUA_GLOBAL_CONFIG</key>") {
		t.Fatalf("plist dropped the passthrough env:\n%s", plist)
	}
	if !strings.Contains(plist, "<string>/home/me/aqua.yaml</string>") {
		t.Fatalf("plist dropped the value:\n%s", plist)
	}
}

// PATH and HOME are written from their own fields; letting Env restate them
// would emit a duplicate key and make the plist invalid.
func TestPlistDoesNotDuplicatePathOrHome(t *testing.T) {
	plist, err := Plist(Options{
		BinaryPath: "/usr/local/bin/reviewd",
		LogsDir:    "/tmp/logs",
		Interval:   180,
		PathEnv:    "/usr/bin",
		Env:        map[string]string{"PATH": "/nope", "HOME": "/nope"},
	})
	if err != nil {
		t.Fatalf("Plist: %v", err)
	}
	if strings.Count(plist, "<key>PATH</key>") != 1 {
		t.Fatalf("PATH appears more than once:\n%s", plist)
	}
	if strings.Contains(plist, "/nope") {
		t.Fatalf("Env overrode PATH/HOME:\n%s", plist)
	}
}

// Regenerating an unchanged install must produce an identical file, or every
// `daemon install` looks like a change.
func TestPlistEnvOrderIsStable(t *testing.T) {
	opts := Options{
		BinaryPath: "/usr/local/bin/reviewd",
		LogsDir:    "/tmp/logs",
		Interval:   180,
		PathEnv:    "/usr/bin",
		Env: map[string]string{
			"AQUA_ROOT_DIR": "/b", "AQUA_GLOBAL_CONFIG": "/a", "AQUA_POLICY_CONFIG": "/c",
		},
	}
	first, err := Plist(opts)
	if err != nil {
		t.Fatalf("Plist: %v", err)
	}
	for range 5 {
		again, err := Plist(opts)
		if err != nil {
			t.Fatalf("Plist: %v", err)
		}
		if again != first {
			t.Fatal("plist generation is not deterministic")
		}
	}
	if !strings.Contains(first, "AQUA_GLOBAL_CONFIG") ||
		strings.Index(first, "AQUA_GLOBAL_CONFIG") > strings.Index(first, "AQUA_ROOT_DIR") {
		t.Fatalf("env keys are not sorted:\n%s", first)
	}
}

func TestCollectEnvSkipsUnsetAndEmpty(t *testing.T) {
	t.Setenv("AQUA_GLOBAL_CONFIG", "/home/me/aqua.yaml")
	t.Setenv("AQUA_ROOT_DIR", "")

	got := CollectEnv()
	if got["AQUA_GLOBAL_CONFIG"] != "/home/me/aqua.yaml" {
		t.Fatalf("AQUA_GLOBAL_CONFIG = %q", got["AQUA_GLOBAL_CONFIG"])
	}
	if _, ok := got["AQUA_ROOT_DIR"]; ok {
		t.Fatal("an empty variable was carried over")
	}
}

// gh reads hosts.yml from XDG_CONFIG_HOME. Dropping it makes the agent look in
// ~/.config/gh, find nothing, and report that gh is not logged in even though
// the same gh works in the shell that installed the agent.
func TestCollectEnvCarriesGhConfigLocation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/home/me/dotfiles")

	if got := CollectEnv()["XDG_CONFIG_HOME"]; got != "/home/me/dotfiles" {
		t.Fatalf("XDG_CONFIG_HOME = %q, want it carried into the plist", got)
	}
}
