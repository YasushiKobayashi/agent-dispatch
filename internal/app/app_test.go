package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YasushiKobayashi/agent-dispatch/internal/config"
	"github.com/YasushiKobayashi/agent-dispatch/internal/ghostty"
	"github.com/YasushiKobayashi/agent-dispatch/internal/lockfile"
	"github.com/YasushiKobayashi/agent-dispatch/internal/model"
	"github.com/YasushiKobayashi/agent-dispatch/internal/notify"
	"github.com/YasushiKobayashi/agent-dispatch/internal/runner"
	"github.com/YasushiKobayashi/agent-dispatch/internal/store"
	"github.com/YasushiKobayashi/agent-dispatch/internal/worktree"
)

// --- fakes -----------------------------------------------------------------

type fakeSource struct {
	events []model.Event
	err    error
	calls  int
}

func (f *fakeSource) ReviewRequested(context.Context, int) ([]model.Event, error) {
	f.calls++
	return f.events, f.err
}

// gitStub stands in for gh and git. `worktree add` creates the directory so the
// runner has somewhere to chdir into.
type gitStub struct{ calls []string }

func (g *gitStub) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	joined := strings.Join(append([]string{name}, args...), " ")
	g.calls = append(g.calls, joined)

	switch {
	case name == "gh" && len(args) > 2 && args[0] == "repo" && args[1] == "clone":
		if err := os.MkdirAll(args[3], 0o755); err != nil {
			return nil, err
		}
		return nil, os.WriteFile(filepath.Join(args[3], "HEAD"), []byte("ref: refs/heads/main\n"), 0o644)
	case strings.Contains(joined, "worktree add"):
		return nil, os.MkdirAll(args[len(args)-2], 0o755)
	}
	return nil, nil
}

// osascriptStub captures the AppleScript reviewd would have run.
type osascriptStub struct {
	scripts []string
	err     error
}

func (o *osascriptStub) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	if name != "/usr/bin/osascript" {
		// Ensure() shells out to `open` first; that is not what we assert on.
		return nil, nil
	}
	o.scripts = append(o.scripts, args[len(args)-1])
	if o.err != nil {
		return nil, o.err
	}
	return []byte("tab-1\tTERM-1\n"), nil
}

type notifyStub struct{ notes []notify.Notification }

func (n *notifyStub) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	n.notes = append(n.notes, notify.Notification{Message: strings.Join(args, " ")})
	return nil, nil
}

// --- helpers ---------------------------------------------------------------

func event(pr int, author string) model.Event {
	return model.Event{
		Kind: model.KindReviewRequested, Repo: "acme/widgets",
		Owner: "acme", Name: "widgets", PRNumber: pr,
		PRTitle: "feat: something", PRURL: "https://github.com/acme/widgets/pull/1",
		Author: author, HeadRefName: "feat/x", HeadSHA: "sha-" + author,
		BaseRefName: "main", Cursor: "sha-" + author, DetectedAt: time.Now(),
	}
}

func rules(t *testing.T, body string) config.Rules {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rules.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadRules(path)
	if err != nil {
		t.Fatalf("LoadRules: %v", err)
	}
	return loaded
}

const reviewRule = `
version: 1
rules:
  - name: review-requested
    on: [review_requested]
    match:
      exclude_authors: ["*[bot]"]
    action:
      prompt: "review {{.Repo}}#{{.PRNumber}}"
      timeout: 20s
`

type harness struct {
	app      *App
	source   *fakeSource
	git      *gitStub
	notifier *notifyStub
	tabs     *osascriptStub
	cfg      config.Config
	store    *store.Store
}

func newHarness(t *testing.T, ruleBody string, events []model.Event, maxJobs int) *harness {
	t.Helper()
	return newHarnessWith(t, ruleBody, events, maxJobs, nil)
}

// newHarnessWith lets a test adjust the configuration before the App is wired,
// which is how the ghostty-mode tests switch modes.
func newHarnessWith(t *testing.T, ruleBody string, events []model.Event, maxJobs int, tweak func(*config.Config)) *harness {
	t.Helper()
	root := t.TempDir()

	cfg := config.Default()
	cfg.Paths.WorkDir = root
	cfg.Paths.DBPath = filepath.Join(root, "state.db")
	cfg.Paths.ReposDir = filepath.Join(root, "repos")
	cfg.Paths.TreesDir = filepath.Join(root, "trees")
	cfg.Paths.RunsDir = filepath.Join(root, "runs")
	cfg.Paths.LockPath = filepath.Join(root, "reviewd.lock")
	cfg.Poll.MaxJobsPerPoll = maxJobs
	if tweak != nil {
		tweak(&cfg)
	}

	st, err := store.Open(cfg.Paths.DBPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	claude := filepath.Join(root, "claude")
	script := "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"指摘なし\"}'\n"
	if err := os.WriteFile(claude, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	git := &gitStub{}
	notifier := &notifyStub{}
	tabs := &osascriptStub{}
	source := &fakeSource{events: events}
	cfg.Claude.Path = claude

	a, err := Open(cfg, rules(t, ruleBody), slog.New(slog.NewTextHandler(io.Discard, nil)), Deps{
		Store:   st,
		Source:  source,
		Trees:   worktree.NewWithRunner(cfg.Paths.ReposDir, cfg.Paths.TreesDir, "gh", git),
		Runner:  runner.New(runner.Options{ClaudePath: claude, RunsDir: cfg.Paths.RunsDir, MaxConcurrent: 2}),
		Ghostty: ghostty.New(ghostty.Options{Window: cfg.Ghostty.Window, Runner: tabs}),
		Notifier: notify.New(notify.Options{
			Enabled: true, Runner: notifier,
			LookPath: func(string) (string, error) { return "", errors.New("not found") },
		}),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return &harness{app: a, source: source, git: git, notifier: notifier, tabs: tabs, cfg: cfg, store: st}
}

func outcomes(report PollReport) map[Outcome]int { return report.Counts() }

// --- tests -----------------------------------------------------------------

func TestPollRunsMatchingEvent(t *testing.T) {
	h := newHarness(t, reviewRule, []model.Event{event(42, "octocat")}, 3)
	ctx := context.Background()

	report, err := h.app.Poll(ctx, PollOptions{})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if report.Detected != 1 {
		t.Fatalf("detected = %d, want 1", report.Detected)
	}
	if got := outcomes(report)[OutcomeRan]; got != 1 {
		t.Fatalf("ran = %d, want 1 (report: %+v)", got, report.Items)
	}
	item := report.Items[0]
	if item.Status != model.RunSuccess {
		t.Errorf("status = %q, want success (err %v)", item.Status, item.Err)
	}
	if item.Rule != "review-requested" {
		t.Errorf("rule = %q", item.Rule)
	}

	// result.md must exist and the run must be recorded with its directory.
	if _, err := os.Stat(filepath.Join(item.OutputDir, "result.md")); err != nil {
		t.Errorf("result.md: %v", err)
	}
	runs, err := h.store.RecentRuns(ctx, 10)
	if err != nil {
		t.Fatalf("RecentRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].Status != model.RunSuccess {
		t.Fatalf("runs = %+v, want one successful run", runs)
	}
	if runs[0].OutputDir != item.OutputDir {
		t.Errorf("run.OutputDir = %q, want %q", runs[0].OutputDir, item.OutputDir)
	}
	if len(h.notifier.notes) == 0 {
		t.Error("no notification was raised")
	}

	// The worktree is cleaned up unless the rule asked to keep it.
	treePath := filepath.Join(h.cfg.Paths.TreesDir, "acme__widgets", "widgets-pr42")
	if _, err := os.Stat(treePath); !os.IsNotExist(err) {
		t.Errorf("worktree survived the run (stat err = %v)", err)
	}
}

func TestPollIsIdempotent(t *testing.T) {
	h := newHarness(t, reviewRule, []model.Event{event(42, "octocat")}, 3)
	ctx := context.Background()

	if _, err := h.app.Poll(ctx, PollOptions{}); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	report, err := h.app.Poll(ctx, PollOptions{})
	if err != nil {
		t.Fatalf("Poll (second): %v", err)
	}
	if got := outcomes(report)[OutcomeProcessed]; got != 1 {
		t.Errorf("already-processed = %d, want 1 (%+v)", got, report.Items)
	}
	if got := outcomes(report)[OutcomeRan]; got != 0 {
		t.Errorf("ran = %d on the second poll, want 0", got)
	}
}

func TestPollRerunsOnANewHeadSHA(t *testing.T) {
	first := event(42, "octocat")
	h := newHarness(t, reviewRule, []model.Event{first}, 3)
	ctx := context.Background()

	if _, err := h.app.Poll(ctx, PollOptions{}); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	// A new commit on the same pull request is a new event.
	pushed := first
	pushed.HeadSHA = "sha-after-push"
	pushed.Cursor = "sha-after-push"
	h.source.events = []model.Event{pushed}

	report, err := h.app.Poll(ctx, PollOptions{})
	if err != nil {
		t.Fatalf("Poll (after push): %v", err)
	}
	if got := outcomes(report)[OutcomeRan]; got != 1 {
		t.Errorf("ran = %d after a new head SHA, want 1 (%+v)", got, report.Items)
	}
}

func TestPollSkipsUnmatchedEventAndExplainsWhy(t *testing.T) {
	h := newHarness(t, reviewRule, []model.Event{event(43, "renovate[bot]")}, 3)

	report, err := h.app.Poll(context.Background(), PollOptions{})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	item := report.Items[0]
	if item.Outcome != OutcomeNoRule {
		t.Fatalf("outcome = %q, want %q", item.Outcome, OutcomeNoRule)
	}
	if len(item.Decisions) == 0 || !strings.Contains(item.Decisions[0].Reason, "exclude_authors") {
		t.Errorf("decisions = %+v, want the exclude_authors reason", item.Decisions)
	}

	// An unmatched event is deliberately left unrecorded so a later rule change
	// can still pick it up.
	if done, err := h.store.IsProcessed(context.Background(), event(43, "renovate[bot]")); err != nil || done {
		t.Errorf("IsProcessed = %v, %v; want false", done, err)
	}
}

func TestPollRespectsMaxJobsPerPoll(t *testing.T) {
	events := []model.Event{event(1, "a"), event(2, "b"), event(3, "c")}
	h := newHarness(t, reviewRule, events, 2)
	ctx := context.Background()

	report, err := h.app.Poll(ctx, PollOptions{})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	counts := outcomes(report)
	if counts[OutcomeRan] != 2 {
		t.Errorf("ran = %d, want 2", counts[OutcomeRan])
	}
	if counts[OutcomeDeferred] != 1 {
		t.Errorf("deferred = %d, want 1", counts[OutcomeDeferred])
	}

	// The deferred event must not be recorded, so the next poll runs it.
	next, err := h.app.Poll(ctx, PollOptions{})
	if err != nil {
		t.Fatalf("Poll (second): %v", err)
	}
	if got := outcomes(next)[OutcomeRan]; got != 1 {
		t.Errorf("ran = %d on the second poll, want the deferred event to run", got)
	}
}

func TestPollDryRunChangesNothing(t *testing.T) {
	h := newHarness(t, reviewRule, []model.Event{event(42, "octocat")}, 3)
	ctx := context.Background()

	report, err := h.app.Poll(ctx, PollOptions{DryRun: true})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if got := outcomes(report)[OutcomeDryRun]; got != 1 {
		t.Fatalf("would-run = %d, want 1 (%+v)", got, report.Items)
	}
	if done, _ := h.store.IsProcessed(ctx, event(42, "octocat")); done {
		t.Error("dry run recorded the event as processed")
	}
	if runs, _ := h.store.RecentRuns(ctx, 10); len(runs) != 0 {
		t.Errorf("dry run recorded %d runs, want 0", len(runs))
	}
	if len(h.git.calls) != 0 {
		t.Errorf("dry run touched git: %v", h.git.calls)
	}
}

func TestSeedRecordsWithoutRunning(t *testing.T) {
	h := newHarness(t, reviewRule, []model.Event{event(42, "octocat")}, 3)
	ctx := context.Background()

	report, err := h.app.Poll(ctx, PollOptions{Seed: true})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if got := outcomes(report)[OutcomeSeeded]; got != 1 {
		t.Fatalf("seeded = %d, want 1 (%+v)", got, report.Items)
	}
	if done, _ := h.store.IsProcessed(ctx, event(42, "octocat")); !done {
		t.Error("seed did not record the event")
	}
	if runs, _ := h.store.RecentRuns(ctx, 10); len(runs) != 0 {
		t.Errorf("seed started %d runs, want 0", len(runs))
	}
}

func TestPollSkipsWhenAnotherPollHoldsTheLock(t *testing.T) {
	h := newHarness(t, reviewRule, []model.Event{event(42, "octocat")}, 3)

	// A Claude Code review easily outlasts the launchd interval, so the next
	// poll must do nothing rather than start a second review of the same PR.
	lock, err := lockfile.Acquire(h.cfg.Paths.LockPath)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer lock.Release()

	report, err := h.app.Poll(context.Background(), PollOptions{})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if !report.Locked {
		t.Fatalf("Locked = false, want true (report: %+v)", report)
	}
	if h.source.calls != 0 {
		t.Errorf("polled GitHub %d times while locked, want 0", h.source.calls)
	}

	// Once the lock is free the poll proceeds normally.
	lock.Release()
	report, err = h.app.Poll(context.Background(), PollOptions{})
	if err != nil {
		t.Fatalf("Poll after release: %v", err)
	}
	if report.Locked || outcomes(report)[OutcomeRan] != 1 {
		t.Errorf("after releasing the lock: %+v", report)
	}
}

// A dry run mutates nothing, so it must not be blocked by a running poll.
func TestDryRunIgnoresTheLock(t *testing.T) {
	h := newHarness(t, reviewRule, []model.Event{event(42, "octocat")}, 3)

	lock, err := lockfile.Acquire(h.cfg.Paths.LockPath)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer lock.Release()

	report, err := h.app.Poll(context.Background(), PollOptions{DryRun: true})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if report.Locked {
		t.Error("a dry run was blocked by the poll lock")
	}
	if outcomes(report)[OutcomeDryRun] != 1 {
		t.Errorf("report = %+v", report.Items)
	}
}

func TestPollPropagatesSourceFailure(t *testing.T) {
	h := newHarness(t, reviewRule, nil, 3)
	h.source.err = errors.New("gh: authentication required")

	if _, err := h.app.Poll(context.Background(), PollOptions{}); err == nil {
		t.Fatal("Poll succeeded despite a source failure")
	}
}

func TestRunOneIgnoresProcessedState(t *testing.T) {
	h := newHarness(t, reviewRule, []model.Event{event(42, "octocat")}, 3)
	ctx := context.Background()

	if _, err := h.app.Poll(ctx, PollOptions{}); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	item, err := h.app.RunOne(ctx, RunOneOptions{Repo: "acme/widgets", PRNumber: 42})
	if err != nil {
		t.Fatalf("RunOne: %v", err)
	}
	if item.Status != model.RunSuccess {
		t.Errorf("status = %q, want success (err %v)", item.Status, item.Err)
	}
	if runs, _ := h.store.RecentRuns(ctx, 10); len(runs) != 2 {
		t.Errorf("recorded %d runs, want 2", len(runs))
	}
}

func TestRunOneRejectsUnknownPR(t *testing.T) {
	h := newHarness(t, reviewRule, []model.Event{event(42, "octocat")}, 3)

	if _, err := h.app.RunOne(context.Background(), RunOneOptions{Repo: "acme/widgets", PRNumber: 9999}); err == nil {
		t.Fatal("RunOne accepted a pull request that is not awaiting review")
	}
}

func TestRunOneRejectsUnknownRule(t *testing.T) {
	h := newHarness(t, reviewRule, []model.Event{event(42, "octocat")}, 3)

	_, err := h.app.RunOne(context.Background(), RunOneOptions{Repo: "acme/widgets", PRNumber: 42, Rule: "no-such-rule"})
	if err == nil || !strings.Contains(err.Error(), "no rule named") {
		t.Fatalf("err = %v, want it to report the unknown rule", err)
	}
}

func TestKeepWorktree(t *testing.T) {
	body := strings.Replace(reviewRule, `      timeout: 20s`, "      timeout: 20s\n      keep_worktree: true", 1)
	h := newHarness(t, body, []model.Event{event(42, "octocat")}, 3)

	if _, err := h.app.Poll(context.Background(), PollOptions{}); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	treePath := filepath.Join(h.cfg.Paths.TreesDir, "acme__widgets", "widgets-pr42")
	if _, err := os.Stat(treePath); err != nil {
		t.Errorf("keep_worktree did not keep the worktree: %v", err)
	}
}

// --- ghostty mode ----------------------------------------------------------

func ghosttyMode(cfg *config.Config) { cfg.Claude.Mode = config.ModeGhostty }

func TestGhosttyModeOpensATabAndRecordsLaunched(t *testing.T) {
	h := newHarnessWith(t, reviewRule, []model.Event{event(42, "octocat")}, 3, ghosttyMode)
	ctx := context.Background()

	report, err := h.app.Poll(ctx, PollOptions{})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	item := report.Items[0]
	if item.Status != model.RunLaunched {
		t.Fatalf("status = %q, want launched (err %v)", item.Status, item.Err)
	}
	if len(h.tabs.scripts) != 1 {
		t.Fatalf("osascript ran %d times, want 1", len(h.tabs.scripts))
	}

	runs, err := h.store.RecentRuns(ctx, 10)
	if err != nil {
		t.Fatalf("RecentRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].Status != model.RunLaunched {
		t.Fatalf("runs = %+v, want one launched run", runs)
	}
}

// The prompt goes to a file the tab reads back, so it never has to survive
// being quoted into both an AppleScript literal and a shell command line.
func TestGhosttyModeWritesThePromptToAFileTheTabReads(t *testing.T) {
	h := newHarnessWith(t, reviewRule, []model.Event{event(42, "octocat")}, 3, ghosttyMode)

	if _, err := h.app.Poll(context.Background(), PollOptions{}); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	script := h.tabs.scripts[0]
	promptPath := ""
	for _, part := range strings.Split(script, "'") {
		if strings.HasSuffix(part, "prompt.md") {
			promptPath = part
			break
		}
	}
	if promptPath == "" {
		t.Fatalf("script does not read a prompt file:\n%s", script)
	}
	body, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatalf("read prompt: %v", err)
	}
	if got, want := strings.TrimSpace(string(body)), "review acme/widgets#42"; got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
}

// Claude Code is still using the worktree in a tab nobody waits for, so
// removing it here would pull the directory out from under the session.
func TestGhosttyModeKeepsTheWorktree(t *testing.T) {
	h := newHarnessWith(t, reviewRule, []model.Event{event(42, "octocat")}, 3, ghosttyMode)

	if _, err := h.app.Poll(context.Background(), PollOptions{}); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	for _, call := range h.git.calls {
		if strings.Contains(call, "worktree remove") {
			t.Fatalf("worktree was removed while the tab is still using it: %q", call)
		}
	}
}

// The tab runs in the worktree, with the interactive args and without the
// headless-only output flags.
func TestGhosttyModeBuildsTheInteractiveCommand(t *testing.T) {
	h := newHarnessWith(t, reviewRule, []model.Event{event(42, "octocat")}, 3, ghosttyMode)

	if _, err := h.app.Poll(context.Background(), PollOptions{}); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	script := h.tabs.scripts[0]
	for _, want := range []string{
		"set initial working directory of cfg to",
		"REVIEWD_PR=42",
		"REVIEWD_RULE=review-requested",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script is missing %q\n---\n%s", want, script)
		}
	}
	for _, unwanted := range []string{"--output-format", "stream-json", "-p "} {
		if strings.Contains(script, unwanted) {
			t.Errorf("interactive command carries the headless-only %q", unwanted)
		}
	}
}

// Ghostty is a GUI app launched by launchd: its own PATH is
// /usr/bin:/bin:/usr/sbin:/sbin, and the tab's command is not run through a
// login shell. Without the environment reviewd resolved, the tab opens and the
// command dies with `claude: not found` before any session exists -- which
// reviewd still records as `launched`, because the tab did open.
func TestGhosttyModeGivesTheTabAnEnvironmentThatCanFindClaude(t *testing.T) {
	t.Setenv("PATH", "/opt/aqua/bin:/usr/bin:/bin")
	t.Setenv("AQUA_GLOBAL_CONFIG", "/home/me/aqua.yaml")
	t.Setenv("XDG_CONFIG_HOME", "/home/me/dotfiles")

	h := newHarnessWith(t, reviewRule, []model.Event{event(42, "octocat")}, 3, ghosttyMode)

	if _, err := h.app.Poll(context.Background(), PollOptions{}); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	script := h.tabs.scripts[0]
	for _, want := range []string{
		"PATH=/opt/aqua/bin:/usr/bin:/bin",
		"AQUA_GLOBAL_CONFIG=/home/me/aqua.yaml",
		"XDG_CONFIG_HOME=/home/me/dotfiles",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("tab environment is missing %q\n---\n%s", want, script)
		}
	}
}

// A tab that cannot be opened must be recorded as a failure, not silently
// counted as a review that happened.
func TestGhosttyModeRecordsFailureWhenTheTabCannotOpen(t *testing.T) {
	h := newHarnessWith(t, reviewRule, []model.Event{event(42, "octocat")}, 3, ghosttyMode)
	h.tabs.err = errors.New("-1743: Not authorized to send Apple events")

	report, err := h.app.Poll(context.Background(), PollOptions{})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	item := report.Items[0]
	if item.Status != model.RunFailed {
		t.Fatalf("status = %q, want failed", item.Status)
	}
	if item.Err == nil || !strings.Contains(item.Err.Error(), "Not authorized") {
		t.Fatalf("error lost the cause: %v", item.Err)
	}
}

// A rule may opt into ghostty while the configured default stays headless.
func TestRuleModeOverridesTheConfiguredDefault(t *testing.T) {
	const ruleBody = `
version: 1
rules:
  - name: review-requested
    on: [review_requested]
    action:
      prompt: "review {{.Repo}}#{{.PRNumber}}"
      mode: ghostty
      timeout: 20s
`
	h := newHarness(t, ruleBody, []model.Event{event(42, "octocat")}, 3)
	if h.cfg.Claude.Mode != config.ModeHeadless {
		t.Fatalf("precondition: config mode = %q, want headless", h.cfg.Claude.Mode)
	}

	report, err := h.app.Poll(context.Background(), PollOptions{})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if report.Items[0].Status != model.RunLaunched {
		t.Fatalf("status = %q, want launched", report.Items[0].Status)
	}
}

// `reviewd run --mode headless` has to win over a rule that says ghostty.
func TestRunOneModeOverridesTheRule(t *testing.T) {
	const ruleBody = `
version: 1
rules:
  - name: review-requested
    on: [review_requested]
    action:
      prompt: "review {{.Repo}}#{{.PRNumber}}"
      mode: ghostty
      timeout: 20s
`
	h := newHarness(t, ruleBody, []model.Event{event(42, "octocat")}, 3)

	item, err := h.app.RunOne(context.Background(), RunOneOptions{
		Repo: "acme/widgets", PRNumber: 42, Mode: config.ModeHeadless,
	})
	if err != nil {
		t.Fatalf("RunOne: %v", err)
	}
	if item.Status != model.RunSuccess {
		t.Fatalf("status = %q, want success (err %v)", item.Status, item.Err)
	}
	if len(h.tabs.scripts) != 0 {
		t.Fatal("headless override still opened a Ghostty tab")
	}
}

func TestRunOneRejectsAnUnknownMode(t *testing.T) {
	h := newHarness(t, reviewRule, []model.Event{event(42, "octocat")}, 3)

	_, err := h.app.RunOne(context.Background(), RunOneOptions{
		Repo: "acme/widgets", PRNumber: 42, Mode: "tmux",
	})
	if err == nil || !strings.Contains(err.Error(), "tmux") {
		t.Fatalf("err = %v, want a complaint about the unknown mode", err)
	}
}

// Remote Control is on by default so a review reviewd opened on this Mac can be
// driven from elsewhere without remembering to switch anything on.
func TestGhosttyModeEnablesRemoteControlByDefault(t *testing.T) {
	h := newHarnessWith(t, reviewRule, []model.Event{event(42, "octocat")}, 3, ghosttyMode)

	if _, err := h.app.Poll(context.Background(), PollOptions{}); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	script := h.tabs.scripts[0]
	if !strings.Contains(script, "--remote-control") {
		t.Fatalf("script does not enable Remote Control:\n%s", script)
	}
	// Named per pull request, or it is indistinguishable from every other
	// session on the machine.
	if !strings.Contains(script, "reviewd-acme__widgets-pr42") {
		t.Fatalf("Remote Control session is not named after the PR:\n%s", script)
	}
}

func TestRemoteControlCanBeSwitchedOff(t *testing.T) {
	off := false
	h := newHarnessWith(t, reviewRule, []model.Event{event(42, "octocat")}, 3, func(cfg *config.Config) {
		cfg.Claude.Mode = config.ModeGhostty
		cfg.Claude.RemoteControl = &off
	})

	if _, err := h.app.Poll(context.Background(), PollOptions{}); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if strings.Contains(h.tabs.scripts[0], "--remote-control") {
		t.Fatal("remote_control: false was ignored")
	}
}

// `claude -p` has no interactive session to control, so the headless path must
// not grow the flag.
func TestHeadlessModeDoesNotPassRemoteControl(t *testing.T) {
	h := newHarness(t, reviewRule, []model.Event{event(42, "octocat")}, 3)

	report, err := h.app.Poll(context.Background(), PollOptions{})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if report.Items[0].Status != model.RunSuccess {
		t.Fatalf("status = %q, want success", report.Items[0].Status)
	}
	if len(h.tabs.scripts) != 0 {
		t.Fatal("headless mode opened a tab")
	}
}

// --model is passed explicitly on every launch. It outranks a settings.json
// `model` and ANTHROPIC_MODEL, so an unattended run cannot be moved onto a
// pricier model by ambient configuration it never sees.
func TestGhosttyModePinsTheModel(t *testing.T) {
	h := newHarnessWith(t, reviewRule, []model.Event{event(42, "octocat")}, 3, ghosttyMode)

	if _, err := h.app.Poll(context.Background(), PollOptions{}); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	// Single quotes arrive doubly escaped: shell quoting, then AppleScript.
	if !strings.Contains(h.tabs.scripts[0], "--model ") ||
		!strings.Contains(h.tabs.scripts[0], "default") {
		t.Fatalf("script does not pin the model:\n%s", h.tabs.scripts[0])
	}
	if !strings.Contains(h.tabs.scripts[0], "--model "+`'\\''default'\\''`) {
		t.Fatalf("model is not quoted as a single argument:\n%s", h.tabs.scripts[0])
	}
}

func TestModelCanBeLeftToTheSurroundingConfiguration(t *testing.T) {
	h := newHarnessWith(t, reviewRule, []model.Event{event(42, "octocat")}, 3, func(cfg *config.Config) {
		cfg.Claude.Mode = config.ModeGhostty
		cfg.Claude.Model = ""
	})

	if _, err := h.app.Poll(context.Background(), PollOptions{}); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if strings.Contains(h.tabs.scripts[0], "--model") {
		t.Fatal("an empty model still passed --model")
	}
}

// --- action.workdir --------------------------------------------------------

// A rule may pin the working directory. Claude Code resolves local-scope MCP
// servers and .mcp.json approval by the directory the session starts in, so a
// fresh worktree path silently loses both; starting in the existing clone keeps
// them and the prompt moves the work into a worktree.
func TestWorkDirSkipsWorktreePreparation(t *testing.T) {
	clone := t.TempDir()
	h := newHarnessWith(t, workDirRule(clone), []model.Event{event(42, "octocat")}, 3, ghosttyMode)

	if _, err := h.app.Poll(context.Background(), PollOptions{}); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	for _, call := range h.git.calls {
		if strings.Contains(call, "worktree add") || strings.Contains(call, "repo clone") {
			t.Fatalf("reviewd prepared a worktree despite action.workdir: %q", call)
		}
	}
	if !strings.Contains(h.tabs.scripts[0], clone) {
		t.Fatalf("tab did not start in the configured directory:\n%s", h.tabs.scripts[0])
	}
}

// The path is a template over the event, so one rule can serve many clones.
func TestWorkDirIsATemplate(t *testing.T) {
	root := t.TempDir()
	clone := filepath.Join(root, "widgets")
	if err := os.MkdirAll(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	rule := strings.Replace(workDirRule(root), "workdir: "+root, "workdir: "+root+"/{{.Name}}", 1)

	h := newHarnessWith(t, rule, []model.Event{event(42, "octocat")}, 3, ghosttyMode)
	if _, err := h.app.Poll(context.Background(), PollOptions{}); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if !strings.Contains(h.tabs.scripts[0], clone) {
		t.Fatalf("workdir template did not expand to %s:\n%s", clone, h.tabs.scripts[0])
	}
}

// A path that does not exist has to fail loudly. Silently falling back to a
// worktree would hand the user a session without the MCP servers they set the
// option for in the first place.
func TestWorkDirMustExist(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-cloned-yet")
	h := newHarnessWith(t, workDirRule(missing), []model.Event{event(42, "octocat")}, 3, ghosttyMode)

	report, err := h.app.Poll(context.Background(), PollOptions{})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	item := report.Items[0]
	if item.Status != model.RunFailed {
		t.Fatalf("status = %q, want failed", item.Status)
	}
	if item.Err == nil || !strings.Contains(item.Err.Error(), "not-cloned-yet") {
		t.Fatalf("error does not name the missing directory: %v", item.Err)
	}
	if len(h.tabs.scripts) != 0 {
		t.Fatal("a tab was opened for a directory that does not exist")
	}
}

func TestWorkDirRejectsAFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := newHarnessWith(t, workDirRule(file), []model.Event{event(42, "octocat")}, 3, ghosttyMode)

	report, err := h.app.Poll(context.Background(), PollOptions{})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if !strings.Contains(errText(report.Items[0].Err), "not a directory") {
		t.Fatalf("err = %v, want a complaint about it not being a directory", report.Items[0].Err)
	}
}

// Without the option nothing changes: reviewd still prepares its own worktree.
func TestWithoutWorkDirTheWorktreeIsStillPrepared(t *testing.T) {
	h := newHarnessWith(t, reviewRule, []model.Event{event(42, "octocat")}, 3, ghosttyMode)

	if _, err := h.app.Poll(context.Background(), PollOptions{}); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	var prepared bool
	for _, call := range h.git.calls {
		if strings.Contains(call, "worktree add") {
			prepared = true
		}
	}
	if !prepared {
		t.Fatalf("no worktree was prepared: %q", h.git.calls)
	}
}

func workDirRule(dir string) string {
	return `
version: 1
rules:
  - name: review-requested
    on: [review_requested]
    action:
      prompt: "review {{.Repo}}#{{.PRNumber}}"
      workdir: ` + dir + `
      timeout: 20s
`
}
