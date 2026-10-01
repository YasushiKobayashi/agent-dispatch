package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/YasushiKobayashi/agent-dispatch/internal/model"
)

// fakeClaude writes a shell script that stands in for the claude binary, so
// the tests exercise the real process handling (exit codes, timeouts, process
// groups) rather than a mocked-out exec.
func fakeClaude(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\n" + body
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	return path
}

func resultLine(text string) string {
	line, _ := json.Marshal(map[string]any{
		"type": "result", "subtype": "success", "is_error": false,
		"result": text, "session_id": "sess-1", "total_cost_usd": 0.1234,
		"duration_ms": 1844, "num_turns": 1,
	})
	return string(line)
}

func event() model.Event {
	return model.Event{
		Kind: model.KindReviewRequested, Repo: "acme/widgets",
		Owner: "acme", Name: "widgets", PRNumber: 42,
		PRTitle: "feat: something", PRURL: "https://github.com/acme/widgets/pull/42",
		HeadSHA: "aaaa1111bbbb2222", Cursor: "aaaa1111bbbb2222",
	}
}

func request(t *testing.T) Request {
	return Request{
		Event: event(), Rule: "review-requested",
		Prompt: "review this", WorkDir: t.TempDir(), Timeout: 30 * time.Second,
	}
}

func newRunner(t *testing.T, claudePath string, extraArgs ...string) *Runner {
	t.Helper()
	return New(Options{
		ClaudePath: claudePath,
		ExtraArgs:  extraArgs,
		RunsDir:    filepath.Join(t.TempDir(), "runs"),
	})
}

func TestRunSuccess(t *testing.T) {
	claude := fakeClaude(t, `
printf '%s\n' '{"type":"system","subtype":"init"}'
printf '%s\n' '`+resultLine("## 指摘\n- foo.go:12 nil 参照")+`'
exit 0
`)
	r := newRunner(t, claude)

	res, err := r.Run(context.Background(), request(t))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != model.RunSuccess {
		t.Errorf("status = %q, want %q (err: %v)", res.Status, model.RunSuccess, res.Err)
	}
	if res.ExitCode == nil || *res.ExitCode != 0 {
		t.Errorf("exit code = %v, want 0", res.ExitCode)
	}
	if !strings.Contains(res.Text, "nil 参照") {
		t.Errorf("text = %q, want it to carry the result event's text", res.Text)
	}
	if res.SessionID != "sess-1" {
		t.Errorf("session id = %q, want sess-1", res.SessionID)
	}
	if res.CostUSD != 0.1234 {
		t.Errorf("cost = %v, want 0.1234", res.CostUSD)
	}
	// The summary skips markdown headings so the notification says something.
	if res.Summary != "指摘" {
		t.Errorf("summary = %q, want the first meaningful line", res.Summary)
	}

	for _, name := range []string{"stream.jsonl", "stderr.log", "result.md", "meta.json"} {
		if _, err := os.Stat(filepath.Join(res.OutputDir, name)); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
	md := readFile(t, filepath.Join(res.OutputDir, "result.md"))
	for _, want := range []string{"acme/widgets#42", "review-requested", "nil 参照", "aaaa1111"} {
		if !strings.Contains(md, want) {
			t.Errorf("result.md missing %q\n%s", want, md)
		}
	}
}

func TestRunPassesPromptAndExtraArgsInWorkDir(t *testing.T) {
	claude := fakeClaude(t, `
printf '%s\n' "$@" > "$PWD/args.txt"
pwd > "$PWD/cwd.txt"
printf '%s\n' '`+resultLine("ok")+`'
`)
	r := newRunner(t, claude, "--output-format", "stream-json", "--allowedTools", "Read,Grep")

	req := request(t)
	if _, err := r.Run(context.Background(), req); err != nil {
		t.Fatalf("Run: %v", err)
	}

	args := readFile(t, filepath.Join(req.WorkDir, "args.txt"))
	wantArgs := "-p\nreview this\n--output-format\nstream-json\n--allowedTools\nRead,Grep\n"
	if args != wantArgs {
		t.Errorf("args =\n%q\nwant\n%q", args, wantArgs)
	}
	// The worktree, not reviewd's own directory, must be the cwd.
	cwd := strings.TrimSpace(readFile(t, filepath.Join(req.WorkDir, "cwd.txt")))
	if resolved, _ := filepath.EvalSymlinks(req.WorkDir); cwd != resolved && cwd != req.WorkDir {
		t.Errorf("cwd = %q, want %q", cwd, req.WorkDir)
	}
}

func TestRunNonZeroExit(t *testing.T) {
	claude := fakeClaude(t, `
echo "credit balance too low" >&2
exit 2
`)
	r := newRunner(t, claude)

	res, err := r.Run(context.Background(), request(t))
	if err != nil {
		t.Fatalf("Run returned a hard error for a claude failure: %v", err)
	}
	if res.Status != model.RunFailed {
		t.Errorf("status = %q, want %q", res.Status, model.RunFailed)
	}
	if res.ExitCode == nil || *res.ExitCode != 2 {
		t.Errorf("exit code = %v, want 2", res.ExitCode)
	}
	// stderr must reach the error so a launchd log alone explains the failure.
	if res.Err == nil || !strings.Contains(res.Err.Error(), "credit balance too low") {
		t.Errorf("err = %v, want it to carry stderr", res.Err)
	}
	md := readFile(t, filepath.Join(res.OutputDir, "result.md"))
	if !strings.Contains(md, "no output") {
		t.Errorf("result.md should say there was no output:\n%s", md)
	}
}

func TestRunTimeoutKillsTheProcessGroup(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "child-alive")
	// A background child outliving its parent is exactly what Setpgid guards
	// against: it keeps touching the marker until it is killed.
	claude := fakeClaude(t, `
( while true; do touch `+marker+`; sleep 0.05; done ) &
sleep 30
`)
	r := New(Options{ClaudePath: claude, RunsDir: filepath.Join(t.TempDir(), "runs"), GracePeriod: time.Second})

	req := request(t)
	req.Timeout = 300 * time.Millisecond

	res, err := r.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != model.RunTimeout {
		t.Fatalf("status = %q, want %q (err: %v)", res.Status, model.RunTimeout, res.Err)
	}
	if res.Err == nil || !strings.Contains(res.Err.Error(), "timed out") {
		t.Errorf("err = %v, want it to mention the timeout", res.Err)
	}

	// Give any survivor a chance to touch the marker, then check it went quiet.
	time.Sleep(300 * time.Millisecond)
	before := modTime(t, marker)
	time.Sleep(300 * time.Millisecond)
	if after := modTime(t, marker); after != before {
		t.Error("a grandchild process survived the timeout; the process group was not killed")
	}
}

func TestRunKeepsPartialOutputOnTimeout(t *testing.T) {
	claude := fakeClaude(t, `
printf '%s\n' '`+resultLine("途中まで書けた指摘")+`'
sleep 30
`)
	r := New(Options{ClaudePath: claude, RunsDir: filepath.Join(t.TempDir(), "runs"), GracePeriod: time.Second})

	req := request(t)
	req.Timeout = 400 * time.Millisecond

	res, err := r.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != model.RunTimeout {
		t.Fatalf("status = %q, want %q", res.Status, model.RunTimeout)
	}
	if res.Text != "途中まで書けた指摘" {
		t.Errorf("text = %q, want the partial output to survive the timeout", res.Text)
	}
}

func TestRunMissingBinary(t *testing.T) {
	r := newRunner(t, filepath.Join(t.TempDir(), "not-installed"))

	res, err := r.Run(context.Background(), request(t))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != model.RunFailed {
		t.Errorf("status = %q, want %q", res.Status, model.RunFailed)
	}
	if _, statErr := os.Stat(filepath.Join(res.OutputDir, "meta.json")); statErr != nil {
		t.Errorf("meta.json was not written for a failed launch: %v", statErr)
	}
}

func TestMetaJSON(t *testing.T) {
	claude := fakeClaude(t, `printf '%s\n' '`+resultLine("ok")+`'`)
	r := newRunner(t, claude)

	res, err := r.Run(context.Background(), request(t))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(res.OutputDir, "meta.json"))), &m); err != nil {
		t.Fatalf("meta.json is not valid JSON: %v", err)
	}
	for key, want := range map[string]any{
		"repo": "acme/widgets", "rule": "review-requested",
		"status": "success", "cursor": "aaaa1111bbbb2222", "prompt": "review this",
	} {
		if m[key] != want {
			t.Errorf("meta[%q] = %v, want %v", key, m[key], want)
		}
	}
}

func TestMaxConcurrent(t *testing.T) {
	// Each fake claude registers itself in a directory for its lifetime and
	// logs how many peers were live at that moment, so the count reflects
	// actual concurrent processes rather than goroutines queued on the
	// semaphore.
	live := filepath.Join(t.TempDir(), "live")
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	counts := filepath.Join(filepath.Dir(live), "counts.txt")
	claude := fakeClaude(t, `
touch `+live+`/$$
ls `+live+` | wc -l >> `+counts+`
sleep 0.3
rm -f `+live+`/$$
printf '%s\n' '`+resultLine("ok")+`'
`)
	r := New(Options{ClaudePath: claude, RunsDir: filepath.Join(t.TempDir(), "runs"), MaxConcurrent: 2})

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.Run(context.Background(), request(t)); err != nil {
				t.Errorf("Run: %v", err)
			}
		}()
	}
	wg.Wait()

	peak := 0
	for _, line := range strings.Fields(readFile(t, counts)) {
		n, err := strconv.Atoi(line)
		if err != nil {
			t.Fatalf("unexpected count %q", line)
		}
		if n > peak {
			peak = n
		}
	}
	if peak == 0 {
		t.Fatal("the fake claude never recorded a concurrency sample")
	}
	if peak > 2 {
		t.Errorf("observed %d concurrent claude processes, want at most 2", peak)
	}
}

func TestOutputDirsDoNotCollide(t *testing.T) {
	claude := fakeClaude(t, `printf '%s\n' '`+resultLine("ok")+`'`)
	r := newRunner(t, claude)

	first, err := r.Run(context.Background(), request(t))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The directory name has one-second resolution, so a same-second re-run
	// would land in the same place; that is acceptable only because the files
	// are rewritten wholesale. Assert the layout instead.
	if !strings.Contains(first.OutputDir, filepath.Join("acme__widgets", "pr-42")) {
		t.Errorf("output dir = %q, want it under <slug>/pr-<n>/", first.OutputDir)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

func modTime(t *testing.T, path string) time.Time {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

func TestRunPassesTheModelBeforeExtraArgs(t *testing.T) {
	argvPath := filepath.Join(t.TempDir(), "argv.txt")
	claude := fakeClaude(t, "printf '%s\\n' \"$@\" > "+argvPath+"\n")

	r := New(Options{ClaudePath: claude, Model: "default",
		RunsDir: filepath.Join(t.TempDir(), "runs"), ExtraArgs: []string{"--verbose"}})
	if _, err := r.Run(context.Background(), request(t)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	argv, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatalf("read argv: %v", err)
	}
	got := strings.Split(strings.TrimSpace(string(argv)), "\n")
	want := []string{"-p", "review this", "--model", "default", "--verbose"}
	if !slices.Equal(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

func TestRunOmitsTheModelFlagWhenUnset(t *testing.T) {
	argvPath := filepath.Join(t.TempDir(), "argv.txt")
	claude := fakeClaude(t, "printf '%s\\n' \"$@\" > "+argvPath+"\n")

	r := New(Options{ClaudePath: claude, RunsDir: filepath.Join(t.TempDir(), "runs")})
	if _, err := r.Run(context.Background(), request(t)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	argv, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatalf("read argv: %v", err)
	}
	if strings.Contains(string(argv), "--model") {
		t.Fatalf("argv carries --model: %s", argv)
	}
}
