// Package app wires the pieces together: detect events, decide which rule
// applies, run Claude Code in an isolated worktree, and record what happened.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/YasushiKobayashi/agent-dispatch/internal/config"
	"github.com/YasushiKobayashi/agent-dispatch/internal/execx"
	"github.com/YasushiKobayashi/agent-dispatch/internal/ghostty"
	"github.com/YasushiKobayashi/agent-dispatch/internal/lockfile"
	"github.com/YasushiKobayashi/agent-dispatch/internal/model"
	"github.com/YasushiKobayashi/agent-dispatch/internal/notify"
	"github.com/YasushiKobayashi/agent-dispatch/internal/rule"
	"github.com/YasushiKobayashi/agent-dispatch/internal/runner"
	"github.com/YasushiKobayashi/agent-dispatch/internal/source/github"
	"github.com/YasushiKobayashi/agent-dispatch/internal/store"
	"github.com/YasushiKobayashi/agent-dispatch/internal/worktree"
)

// Source produces the events reviewd reacts to.
type Source interface {
	ReviewRequested(ctx context.Context, limit int) ([]model.Event, error)
}

// App holds the wired dependencies for one command invocation.
type App struct {
	cfg      config.Config
	rules    config.Rules
	log      *slog.Logger
	store    *store.Store
	source   Source
	trees    *worktree.Manager
	runner   *runner.Runner
	ghostty  *ghostty.Launcher
	notifier *notify.Notifier
}

// Deps allows tests to substitute individual collaborators. Any nil field is
// built from cfg.
type Deps struct {
	Store    *store.Store
	Source   Source
	Trees    *worktree.Manager
	Runner   *runner.Runner
	Ghostty  *ghostty.Launcher
	Notifier *notify.Notifier
}

// Open builds an App. The caller must Close it.
func Open(cfg config.Config, rules config.Rules, log *slog.Logger, deps Deps) (*App, error) {
	if log == nil {
		log = slog.Default()
	}

	st := deps.Store
	if st == nil {
		opened, err := store.Open(cfg.Paths.DBPath)
		if err != nil {
			return nil, err
		}
		st = opened
	}

	src := deps.Source
	if src == nil {
		src = github.New(cfg.GitHub.GhPath, cfg.GitHub.Login)
	}
	trees := deps.Trees
	if trees == nil {
		trees = worktree.New(cfg.Paths.ReposDir, cfg.Paths.TreesDir, cfg.GitHub.GhPath)
	}
	run := deps.Runner
	if run == nil {
		run = runner.New(runner.Options{
			ClaudePath:    cfg.Claude.Path,
			Model:         cfg.Claude.Model,
			ExtraArgs:     cfg.Claude.ExtraArgs,
			RunsDir:       cfg.Paths.RunsDir,
			MaxConcurrent: cfg.Claude.MaxConcurrent,
		})
	}
	tabs := deps.Ghostty
	if tabs == nil {
		tabs = ghostty.New(ghostty.Options{
			AppPath:  cfg.Ghostty.AppPath,
			Window:   cfg.Ghostty.Window,
			Activate: cfg.Ghostty.Activate,
		})
	}
	notifier := deps.Notifier
	if notifier == nil {
		notifier = notify.New(notify.Options{
			Enabled:                cfg.Notify.Enabled,
			PreferTerminalNotifier: cfg.Notify.PreferTerminalNotifier,
		})
	}

	return &App{cfg: cfg, rules: rules, log: log, store: st,
		source: src, trees: trees, runner: run, ghostty: tabs, notifier: notifier}, nil
}

// Close releases the store.
func (a *App) Close() error { return a.store.Close() }

// Store exposes the state store for read-only commands such as `status`.
func (a *App) Store() *store.Store { return a.store }

// Outcome is what happened to a single detected event during a poll.
type Outcome string

const (
	// OutcomeProcessed means the event was already acted on in an earlier poll.
	OutcomeProcessed Outcome = "already-processed"
	// OutcomeNoRule means no enabled rule matched.
	OutcomeNoRule Outcome = "no-rule"
	// OutcomeSeeded means the event was recorded without running anything.
	OutcomeSeeded Outcome = "seeded"
	// OutcomeDryRun means the event would have run.
	OutcomeDryRun Outcome = "would-run"
	// OutcomeDeferred means the per-poll job budget was already spent.
	OutcomeDeferred Outcome = "deferred"
	// OutcomeRan means Claude Code was started; see Status for how it ended.
	OutcomeRan Outcome = "ran"
)

// Item records what happened to one event.
type Item struct {
	Event     model.Event
	Rule      string
	Outcome   Outcome
	Status    model.RunStatus
	OutputDir string
	Decisions []rule.Decision
	Err       error
}

// PollReport summarises a poll.
type PollReport struct {
	Detected int
	Items    []Item
	// Locked is true when another poll was already running and this one did
	// nothing.
	Locked bool
}

// Counts returns how many items ended in each outcome.
func (r PollReport) Counts() map[Outcome]int {
	counts := make(map[Outcome]int, len(r.Items))
	for _, item := range r.Items {
		counts[item.Outcome]++
	}
	return counts
}

// PollOptions tunes a single poll.
type PollOptions struct {
	// DryRun detects and matches but neither runs nor records anything.
	DryRun bool
	// Seed records every detected event as processed without running it. Used
	// once at install time so a backlog of open review requests does not all
	// fire at once.
	Seed bool
	// Limit caps how many pull requests the search returns.
	Limit int
}

// Poll detects events, matches them against the rules, and runs what matches.
func (a *App) Poll(ctx context.Context, opts PollOptions) (PollReport, error) {
	var report PollReport

	// A dry run mutates nothing, so it does not need to exclude a real poll.
	if !opts.DryRun {
		lock, err := lockfile.Acquire(a.cfg.Paths.LockPath)
		if errors.Is(err, lockfile.ErrLocked) {
			a.log.Info("another poll is still running; skipping", "lock", a.cfg.Paths.LockPath)
			report.Locked = true
			return report, nil
		}
		if err != nil {
			return report, err
		}
		defer lock.Release()
	}

	events, err := a.source.ReviewRequested(ctx, opts.Limit)
	if err != nil {
		return report, err
	}
	report.Detected = len(events)

	enabled := a.rules.Enabled()
	budget := a.cfg.Poll.MaxJobsPerPoll

	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		started int
	)
	appendItem := func(item Item) {
		mu.Lock()
		report.Items = append(report.Items, item)
		mu.Unlock()
	}

	for _, ev := range events {
		done, err := a.store.IsProcessed(ctx, ev)
		if err != nil {
			return report, err
		}
		if done {
			appendItem(Item{Event: ev, Outcome: OutcomeProcessed})
			continue
		}

		matched, decisions := rule.Match(enabled, ev)
		if matched == nil {
			a.log.Debug("no rule matched", "event", ev.Key())
			appendItem(Item{Event: ev, Outcome: OutcomeNoRule, Decisions: decisions})
			continue
		}

		switch {
		case opts.Seed:
			if err := a.store.MarkProcessed(ctx, ev); err != nil {
				return report, err
			}
			appendItem(Item{Event: ev, Rule: matched.Name, Outcome: OutcomeSeeded})
			continue
		case opts.DryRun:
			appendItem(Item{Event: ev, Rule: matched.Name, Outcome: OutcomeDryRun, Decisions: decisions})
			continue
		}

		if started >= budget {
			// Leave it unrecorded so the next poll picks it up.
			a.log.Info("per-poll job budget reached; deferring",
				"event", ev.Key(), "budget", budget)
			appendItem(Item{Event: ev, Rule: matched.Name, Outcome: OutcomeDeferred})
			continue
		}
		started++

		// Recorded before the run, not after: a run that keeps failing must not
		// be retried on every poll.
		if err := a.store.MarkProcessed(ctx, ev); err != nil {
			return report, err
		}

		ev, matchedRule := ev, *matched
		wg.Add(1)
		go func() {
			defer wg.Done()
			appendItem(a.execute(ctx, ev, matchedRule))
		}()
	}
	wg.Wait()

	return report, nil
}

// RunOneOptions selects the pull request `reviewd run` acts on.
type RunOneOptions struct {
	Repo     string
	PRNumber int
	// Rule, when set, forces a specific rule instead of first-match-wins.
	Rule string
	// Mode, when set, overrides both the rule's and the config's run mode.
	Mode string
}

// RunOne executes a rule against a specific pull request, ignoring whether the
// event was already processed. It is the manual escape hatch for re-running a
// review.
func (a *App) RunOne(ctx context.Context, opts RunOneOptions) (Item, error) {
	repo, prNumber, ruleName := opts.Repo, opts.PRNumber, opts.Rule
	events, err := a.source.ReviewRequested(ctx, 0)
	if err != nil {
		return Item{}, err
	}

	var found *model.Event
	for i := range events {
		if events[i].Repo == repo && events[i].PRNumber == prNumber {
			found = &events[i]
			break
		}
	}
	if found == nil {
		return Item{}, fmt.Errorf("%s#%d is not currently awaiting your review; nothing to run", repo, prNumber)
	}

	candidates := a.rules.Enabled()
	if ruleName != "" {
		candidates = nil
		for _, r := range a.rules.Rules {
			if r.Name == ruleName {
				candidates = append(candidates, r)
			}
		}
		if len(candidates) == 0 {
			return Item{}, fmt.Errorf("no rule named %q", ruleName)
		}
	}

	matched, decisions := rule.Match(candidates, *found)
	if matched == nil {
		return Item{Event: *found, Outcome: OutcomeNoRule, Decisions: decisions},
			fmt.Errorf("no rule matches %s#%d", repo, prNumber)
	}

	if err := a.store.MarkProcessed(ctx, *found); err != nil {
		return Item{}, err
	}

	chosen := *matched
	if strings.TrimSpace(opts.Mode) != "" {
		if !config.ValidMode(strings.ToLower(opts.Mode)) {
			return Item{}, fmt.Errorf("unknown mode %q", opts.Mode)
		}
		chosen.Action.Mode = strings.ToLower(opts.Mode)
	}
	return a.execute(ctx, *found, chosen), nil
}

// execute prepares a worktree, runs Claude Code, records the run and notifies.
func (a *App) execute(ctx context.Context, ev model.Event, r config.Rule) Item {
	item := Item{Event: ev, Rule: r.Name, Outcome: OutcomeRan, Status: model.RunFailed}
	log := a.log.With("event", ev.Key(), "rule", r.Name)

	runID, err := a.store.StartRun(ctx, ev, r.Name, "")
	if err != nil {
		item.Err = err
		return item
	}

	// finish records the outcome exactly once, whichever way execute returns.
	finish := func(status model.RunStatus, exitCode *int, cause error) Item {
		item.Status = status
		item.Err = cause
		if err := a.store.FinishRun(ctx, runID, status, exitCode, item.OutputDir, errText(cause)); err != nil {
			log.Error("failed to record run outcome", "err", err)
		}
		return item
	}

	prompt, err := r.RenderPrompt(ev)
	if err != nil {
		return finish(model.RunFailed, nil, err)
	}

	mode := r.ModeOr(a.cfg.Claude.Mode)

	// A rule may pin the working directory, in which case reviewd prepares
	// nothing and the prompt is responsible for isolating the work. See
	// config.Action.WorkDir for why anyone would want that.
	workDir, err := r.RenderWorkDir(ev)
	if err != nil {
		return finish(model.RunFailed, nil, err)
	}
	if workDir != "" {
		info, serr := os.Stat(workDir)
		switch {
		case serr != nil:
			return finish(model.RunFailed, nil,
				fmt.Errorf("rule %q: action.workdir %s: %w", r.Name, workDir, serr))
		case !info.IsDir():
			return finish(model.RunFailed, nil,
				fmt.Errorf("rule %q: action.workdir %s is not a directory", r.Name, workDir))
		}
		log.Info("using the configured working directory", "workdir", workDir, "mode", mode)
	} else {
		log.Info("preparing worktree", "pr", ev.PRURL, "head", ev.HeadSHA, "mode", mode)
		tree, terr := a.trees.Prepare(ctx, ev)
		if terr != nil {
			return finish(model.RunFailed, nil, terr)
		}
		workDir = tree.Path
		// In ghostty mode the worktree outlives this process: Claude Code is
		// still using it in a tab we do not wait for. Removing it here would
		// pull the directory out from under the session just handed to the user.
		if !r.Action.KeepWorktree && mode != config.ModeGhostty {
			defer func() {
				// Detached from ctx on purpose: a cancelled run must still clean
				// up, or the leftover worktree breaks the next attempt.
				if err := tree.Remove(context.WithoutCancel(ctx)); err != nil {
					log.Warn("failed to remove worktree", "path", tree.Path, "err", err)
				}
			}()
		}
	}

	if mode == config.ModeGhostty {
		tab, lerr := a.launchGhostty(ctx, ev, r, prompt, workDir)
		if lerr != nil {
			return finish(model.RunFailed, nil, lerr)
		}
		log.Info("handed off to a Ghostty tab", "tab", tab.TabID, "workdir", workDir)

		if r.ShouldNotify() {
			if err := a.notifier.Notify(ctx, notify.Notification{
				Title:    "reviewd: launched",
				Subtitle: fmt.Sprintf("%s#%d", ev.Repo, ev.PRNumber),
				Message:  "Ghostty タブでレビューを開始しました: " + ev.PRTitle,
			}); err != nil {
				log.Warn("notification failed", "err", err)
			}
		}
		return finish(model.RunLaunched, nil, nil)
	}

	timeout := r.Action.Timeout.D()
	if timeout <= 0 {
		timeout = a.cfg.Claude.Timeout.D()
	}

	log.Info("running claude", "workdir", workDir, "timeout", timeout)
	res, err := a.runner.Run(ctx, runner.Request{
		Event: ev, Rule: r.Name, Prompt: prompt, WorkDir: workDir, Timeout: timeout,
	})
	item.OutputDir = res.OutputDir
	if err != nil {
		return finish(model.RunFailed, res.ExitCode, err)
	}

	log.Info("claude finished",
		"status", res.Status, "duration", res.Duration.Round(time.Second), "out", res.OutputDir)

	if r.ShouldNotify() {
		if err := a.notifier.Notify(ctx, notify.Notification{
			Title:    "reviewd: " + string(res.Status),
			Subtitle: fmt.Sprintf("%s#%d", ev.Repo, ev.PRNumber),
			Message:  res.Summary,
			OpenPath: resultPath(res.OutputDir),
		}); err != nil {
			log.Warn("notification failed", "err", err)
		}
	}

	return finish(res.Status, res.ExitCode, res.Err)
}

// launchGhostty opens a Ghostty tab running Claude Code interactively in the
// worktree, and returns as soon as the tab exists.
//
// The prompt is written to a file and read back by the shell rather than being
// interpolated into the command line: a review prompt is multi-line and full of
// backticks, and this keeps it out of both the AppleScript literal and the
// shell's own parsing.
func (a *App) launchGhostty(ctx context.Context, ev model.Event, r config.Rule, prompt, workDir string) (ghostty.Tab, error) {
	outDir := a.runner.OutputDir(ev)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return ghostty.Tab{}, fmt.Errorf("create %s: %w", outDir, err)
	}
	promptPath := filepath.Join(outDir, "prompt.md")
	if err := os.WriteFile(promptPath, []byte(prompt+"\n"), 0o644); err != nil {
		return ghostty.Tab{}, fmt.Errorf("write %s: %w", promptPath, err)
	}

	parts := make([]string, 0, len(a.cfg.Claude.InteractiveArgs)+5)
	parts = append(parts, "exec", ghostty.ShellQuote(a.cfg.Claude.Path))
	if a.cfg.Claude.Model != "" {
		parts = append(parts, "--model", ghostty.ShellQuote(a.cfg.Claude.Model))
	}
	for _, arg := range a.cfg.Claude.InteractiveArgs {
		parts = append(parts, ghostty.ShellQuote(arg))
	}
	if a.cfg.Claude.ShouldRemoteControl() {
		// Named per pull request: the point of Remote Control is picking this
		// session out from every other one on the machine.
		parts = append(parts, "--remote-control", ghostty.ShellQuote(remoteControlName(ev)))
	}
	// Expanded by the inner sh, so the prompt never has to be quoted twice.
	parts = append(parts, `"$(cat `+ghostty.ShellQuote(promptPath)+`)"`)

	// Starting the app first keeps the AppleScript from racing a cold start.
	// Not fatal on its own: `tell application "Ghostty"` launches it too.
	if err := a.ghostty.Ensure(ctx); err != nil {
		a.log.Warn("could not pre-launch Ghostty", "err", err)
	}

	// Ghostty is a GUI app started by launchd, so it inherits
	// PATH=/usr/bin:/bin:/usr/sbin:/sbin and the tab's command is run directly
	// rather than through a login shell. A bare `claude` there dies with
	// `claude: not found` before the session exists, and the tab only shows it
	// if keep_open is on. Hand the tab the environment reviewd itself resolved.
	env := append(execx.ToolEnviron(),
		"REVIEWD_REPO="+ev.Repo,
		"REVIEWD_PR="+strconv.Itoa(ev.PRNumber),
		"REVIEWD_RULE="+r.Name,
		"REVIEWD_RUN_DIR="+outDir,
	)

	tab, err := a.ghostty.Launch(ctx, ghostty.Request{
		Command:  ghostty.Sh(strings.Join(parts, " ")),
		WorkDir:  workDir,
		KeepOpen: a.cfg.Ghostty.ShouldKeepOpen(),
		Env:      env,
	})
	if err != nil {
		return ghostty.Tab{}, err
	}

	// The output dir holds only the prompt in this mode, but recording it means
	// `reviewd status` still points at something useful.
	a.log.Debug("ghostty tab opened", "tab", tab.TabID, "prompt", promptPath)
	return tab, nil
}

// remoteControlName labels the Remote Control session. Kept to the characters
// Event.Slug already restricts itself to, so nothing downstream has to quote it.
func remoteControlName(ev model.Event) string {
	return "reviewd-" + ev.Slug() + "-pr" + strconv.Itoa(ev.PRNumber)
}

func resultPath(outputDir string) string {
	if outputDir == "" {
		return ""
	}
	return filepath.Join(outputDir, "result.md")
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
