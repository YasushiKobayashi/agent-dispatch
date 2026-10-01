// Package runner executes Claude Code headlessly against a prepared worktree
// and persists everything the run produced.
//
// Unix only: the runner puts Claude Code in its own process group so a timeout
// takes the whole tree down rather than orphaning tool subprocesses.
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/YasushiKobayashi/agent-dispatch/internal/model"
)

// Options configures a Runner.
type Options struct {
	// ClaudePath is the claude executable. Defaults to "claude".
	ClaudePath string
	// Model, when set, is passed as --model. It goes before ExtraArgs so a
	// user who pins --model there still wins.
	Model string
	// ExtraArgs are appended to every invocation (output format, permission
	// mode, and anything else the user pinned in config.yaml).
	ExtraArgs []string
	// RunsDir is the root under which each run gets its own directory.
	RunsDir string
	// MaxConcurrent bounds simultaneous Claude Code processes. Defaults to 1.
	MaxConcurrent int
	// Now is injectable for deterministic output directory names in tests.
	Now func() time.Time
	// GracePeriod is how long a cancelled process gets to exit before it is
	// killed outright. Defaults to 10s.
	GracePeriod time.Duration
}

// Runner executes Claude Code runs, at most MaxConcurrent at a time.
type Runner struct {
	opts Options
	sem  chan struct{}
}

// New returns a Runner.
func New(opts Options) *Runner {
	if opts.ClaudePath == "" {
		opts.ClaudePath = "claude"
	}
	if opts.MaxConcurrent <= 0 {
		opts.MaxConcurrent = 1
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.GracePeriod <= 0 {
		opts.GracePeriod = 10 * time.Second
	}
	return &Runner{opts: opts, sem: make(chan struct{}, opts.MaxConcurrent)}
}

// Request is one Claude Code invocation.
type Request struct {
	Event   model.Event
	Rule    string
	Prompt  string
	WorkDir string
	Timeout time.Duration
}

// Result describes how a run ended. It is returned even for failures, so the
// caller can record the output directory and notify about what went wrong.
type Result struct {
	OutputDir string
	Status    model.RunStatus
	ExitCode  *int
	// Text is Claude Code's final answer, empty if the run produced none.
	Text      string
	Summary   string
	SessionID string
	CostUSD   float64
	Duration  time.Duration
	Err       error
}

// resultEvent is the final line of `--output-format stream-json` output.
type resultEvent struct {
	Type       string  `json:"type"`
	Subtype    string  `json:"subtype"`
	IsError    bool    `json:"is_error"`
	Result     string  `json:"result"`
	SessionID  string  `json:"session_id"`
	TotalCost  float64 `json:"total_cost_usd"`
	DurationMS int64   `json:"duration_ms"`
	NumTurns   int     `json:"num_turns"`
}

// Run executes Claude Code for the request and writes stream.jsonl,
// stderr.log, result.md and meta.json into a fresh output directory.
//
// An error is returned only when the run could not be carried out; a Claude
// Code failure is reported through Result.Status instead so the caller can
// still surface the output.
func (r *Runner) Run(ctx context.Context, req Request) (Result, error) {
	select {
	case r.sem <- struct{}{}:
		defer func() { <-r.sem }()
	case <-ctx.Done():
		return Result{Status: model.RunSkipped}, ctx.Err()
	}

	outDir := r.OutputDir(req.Event)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return Result{Status: model.RunFailed}, fmt.Errorf("create %s: %w", outDir, err)
	}
	res := Result{OutputDir: outDir, Status: model.RunFailed}

	streamPath := filepath.Join(outDir, "stream.jsonl")
	stream, err := os.Create(streamPath)
	if err != nil {
		return res, fmt.Errorf("create %s: %w", streamPath, err)
	}
	defer stream.Close()

	stderrPath := filepath.Join(outDir, "stderr.log")
	stderr, err := os.Create(stderrPath)
	if err != nil {
		return res, fmt.Errorf("create %s: %w", stderrPath, err)
	}
	defer stderr.Close()

	runCtx := ctx
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	args := []string{"-p", req.Prompt}
	if r.opts.Model != "" {
		args = append(args, "--model", r.opts.Model)
	}
	args = append(args, r.opts.ExtraArgs...)
	cmd := exec.CommandContext(runCtx, r.opts.ClaudePath, args...)
	cmd.Dir = req.WorkDir
	cmd.Stdout = stream
	cmd.Stderr = stderr
	// Own process group, so cancelling reaches the tools Claude Code spawned.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	cmd.WaitDelay = r.opts.GracePeriod

	started := r.opts.Now()
	runErr := cmd.Run()
	res.Duration = r.opts.Now().Sub(started)

	if cmd.ProcessState != nil {
		code := cmd.ProcessState.ExitCode()
		res.ExitCode = &code
	}

	// Read the result event even on failure: a run killed by a timeout still
	// leaves whatever Claude Code managed to emit.
	if ev, ok := readResultEvent(streamPath); ok {
		res.Text = strings.TrimSpace(ev.Result)
		res.SessionID = ev.SessionID
		res.CostUSD = ev.TotalCost
	}

	switch {
	case runErr == nil:
		res.Status = model.RunSuccess
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		res.Status = model.RunTimeout
		res.Err = fmt.Errorf("claude timed out after %s", req.Timeout)
	case errors.Is(ctx.Err(), context.Canceled):
		res.Status = model.RunSkipped
		res.Err = fmt.Errorf("cancelled: %w", ctx.Err())
	default:
		res.Status = model.RunFailed
		res.Err = fmt.Errorf("claude: %w%s", runErr, stderrHint(stderrPath))
	}
	res.Summary = summarize(res)

	if err := r.writeResultMarkdown(outDir, req, res); err != nil {
		return res, err
	}
	if err := writeMeta(outDir, req, res); err != nil {
		return res, err
	}
	return res, nil
}

// OutputDir is the directory a run for this event would write to. The
// timestamp keeps re-runs of the same pull request from overwriting each other.
func (r *Runner) OutputDir(ev model.Event) string {
	return filepath.Join(r.opts.RunsDir, ev.Slug(), "pr-"+strconv.Itoa(ev.PRNumber),
		r.opts.Now().Format("20060102-150405"))
}

func (r *Runner) writeResultMarkdown(outDir string, req Request, res Result) error {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s#%d\n\n", req.Event.Repo, req.Event.PRNumber)
	if req.Event.PRTitle != "" {
		fmt.Fprintf(&sb, "%s\n\n", req.Event.PRTitle)
	}
	if req.Event.PRURL != "" {
		fmt.Fprintf(&sb, "%s\n\n", req.Event.PRURL)
	}
	fmt.Fprintf(&sb, "- rule: `%s`\n- event: `%s`\n- head: `%s`\n- status: `%s`\n- duration: %s\n",
		req.Rule, req.Event.Kind, shortSHA(req.Event.HeadSHA), res.Status, res.Duration.Round(time.Second))
	if res.CostUSD > 0 {
		fmt.Fprintf(&sb, "- cost: $%.4f\n", res.CostUSD)
	}
	sb.WriteString("\n---\n\n")

	switch {
	case res.Text != "":
		sb.WriteString(res.Text)
		sb.WriteString("\n")
	case res.Err != nil:
		fmt.Fprintf(&sb, "Claude Code produced no output.\n\n```\n%v\n```\n", res.Err)
	default:
		sb.WriteString("Claude Code produced no output.\n")
	}

	path := filepath.Join(outDir, "result.md")
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

type meta struct {
	Repo      string      `json:"repo"`
	PRNumber  int         `json:"pr_number"`
	PRURL     string      `json:"pr_url"`
	Kind      string      `json:"kind"`
	Cursor    string      `json:"cursor"`
	Rule      string      `json:"rule"`
	Status    string      `json:"status"`
	ExitCode  *int        `json:"exit_code"`
	SessionID string      `json:"session_id,omitempty"`
	CostUSD   float64     `json:"total_cost_usd,omitempty"`
	Duration  string      `json:"duration"`
	WorkDir   string      `json:"work_dir"`
	Prompt    string      `json:"prompt"`
	Error     string      `json:"error,omitempty"`
	Event     model.Event `json:"event"`
}

func writeMeta(outDir string, req Request, res Result) error {
	m := meta{
		Repo: req.Event.Repo, PRNumber: req.Event.PRNumber, PRURL: req.Event.PRURL,
		Kind: string(req.Event.Kind), Cursor: req.Event.Cursor, Rule: req.Rule,
		Status: string(res.Status), ExitCode: res.ExitCode, SessionID: res.SessionID,
		CostUSD: res.CostUSD, Duration: res.Duration.String(),
		WorkDir: req.WorkDir, Prompt: req.Prompt, Event: req.Event,
	}
	if res.Err != nil {
		m.Error = res.Err.Error()
	}

	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode meta.json: %w", err)
	}
	path := filepath.Join(outDir, "meta.json")
	if err := os.WriteFile(path, append(body, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// readResultEvent finds the last `{"type":"result"}` line in the stream.
func readResultEvent(path string) (resultEvent, bool) {
	body, err := os.ReadFile(path)
	if err != nil {
		return resultEvent{}, false
	}

	lines := strings.Split(string(body), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || !strings.Contains(line, `"result"`) {
			continue
		}
		var ev resultEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil || ev.Type != "result" {
			continue
		}
		return ev, true
	}
	return resultEvent{}, false
}

// summarize produces the one-line body of the macOS notification.
func summarize(res Result) string {
	if res.Status != model.RunSuccess {
		if res.Err != nil {
			return truncate(firstLine(res.Err.Error()), 120)
		}
		return string(res.Status)
	}
	if res.Text == "" {
		return "完了（出力なし）"
	}
	return truncate(firstLine(res.Text), 120)
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(line, "#-* "))
		if line != "" {
			return line
		}
	}
	return strings.TrimSpace(s)
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

// stderrHint appends a short excerpt of stderr so a failure is diagnosable
// straight from the log line.
func stderrHint(path string) string {
	body, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		return ""
	}
	return ": " + truncate(firstLine(msg), 200)
}

func shortSHA(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
