// Package worktree materialises a pull request's head commit on disk without
// touching any repository the user is working in.
//
// Each repository gets a dedicated bare mirror under reposDir, and each run
// gets a detached git worktree under treesDir. The user's own clones are never
// read, fetched into, or checked out.
package worktree

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/YasushiKobayashi/agent-dispatch/internal/execx"
	"github.com/YasushiKobayashi/agent-dispatch/internal/model"
)

// Manager owns the mirror and worktree directories.
type Manager struct {
	reposDir string
	treesDir string
	gitPath  string
	ghPath   string
	runner   execx.Runner
}

// New returns a Manager writing under reposDir and treesDir.
func New(reposDir, treesDir, ghPath string) *Manager {
	return NewWithRunner(reposDir, treesDir, ghPath, execx.Exec{})
}

// NewWithRunner is New with an injectable command runner, for tests.
func NewWithRunner(reposDir, treesDir, ghPath string, runner execx.Runner) *Manager {
	if ghPath == "" {
		ghPath = "gh"
	}
	return &Manager{reposDir: reposDir, treesDir: treesDir, gitPath: "git", ghPath: ghPath, runner: runner}
}

// Tree is a prepared worktree. Call Remove when the run is done.
type Tree struct {
	// Path is the directory Claude Code should run in.
	Path string
	// MirrorPath is the bare repository backing the worktree.
	MirrorPath string
	// BaseRef is a ref name usable in the worktree for diffing against the
	// pull request's base branch, e.g. "refs/remotes/origin/main".
	BaseRef string

	manager *Manager
}

// Prepare produces a worktree checked out at the event's head commit.
//
// The head commit is fetched through refs/pull/<n>/head rather than the branch
// name, so pull requests opened from forks work exactly like same-repo ones.
func (m *Manager) Prepare(ctx context.Context, ev model.Event) (*Tree, error) {
	mirror := m.mirrorPath(ev)
	if err := m.ensureMirror(ctx, ev, mirror); err != nil {
		return nil, err
	}

	pullRef := fmt.Sprintf("refs/reviewd/pr/%d", ev.PRNumber)
	fetchSpecs := []string{
		fmt.Sprintf("+refs/pull/%d/head:%s", ev.PRNumber, pullRef),
	}
	baseRef := ""
	if ev.BaseRefName != "" {
		baseRef = "refs/remotes/origin/" + ev.BaseRefName
		fetchSpecs = append(fetchSpecs, fmt.Sprintf("+refs/heads/%s:%s", ev.BaseRefName, baseRef))
	}
	if err := m.git(ctx, mirror, append([]string{"fetch", "--prune", "--no-tags", "origin"}, fetchSpecs...)...); err != nil {
		return nil, fmt.Errorf("fetch %s#%d: %w", ev.Repo, ev.PRNumber, err)
	}

	path := m.TreePath(ev)
	if err := m.clearTree(ctx, mirror, path); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}

	// Check out the exact SHA the event was raised for, not the ref: if the PR
	// was force-pushed since detection, reviewing the newer commit would make
	// the run disagree with the cursor recorded in the store.
	commit := ev.HeadSHA
	if commit == "" {
		commit = pullRef
	}
	if err := m.git(ctx, mirror, "worktree", "add", "--detach", "--force", path, commit); err != nil {
		return nil, fmt.Errorf("create worktree for %s#%d at %s: %w", ev.Repo, ev.PRNumber, shortSHA(commit), err)
	}

	return &Tree{Path: path, MirrorPath: mirror, BaseRef: baseRef, manager: m}, nil
}

// Remove deletes the worktree and prunes the mirror's administrative record of
// it. It is safe to call on an already-removed tree.
func (t *Tree) Remove(ctx context.Context) error {
	if t == nil || t.manager == nil {
		return nil
	}
	return t.manager.clearTree(ctx, t.MirrorPath, t.Path)
}

// TreePath is where the worktree for this event lives. The leaf is named
// <repo>-pr<N> because a terminal tab titles itself after the directory it sits
// in, and "pr-42" alone does not say which repository it belongs to.
func (m *Manager) TreePath(ev model.Event) string {
	return filepath.Join(m.treesDir, ev.Slug(), ev.TreeName())
}

func (m *Manager) mirrorPath(ev model.Event) string {
	return filepath.Join(m.reposDir, ev.Slug()+".git")
}

// ensureMirror clones the bare mirror on first use and makes sure it can
// authenticate on its own afterwards.
func (m *Manager) ensureMirror(ctx context.Context, ev model.Event, mirror string) error {
	if _, err := os.Stat(filepath.Join(mirror, "HEAD")); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(mirror), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(mirror), err)
	}
	// A partial directory from an interrupted clone would make gh fail.
	if err := os.RemoveAll(mirror); err != nil {
		return fmt.Errorf("clear %s: %w", mirror, err)
	}

	if _, err := m.runner.Run(ctx, m.ghPath, "repo", "clone", ev.Repo, mirror, "--", "--bare"); err != nil {
		return fmt.Errorf("clone %s: %w", ev.Repo, err)
	}

	// gh only authenticates the clone itself. Pin the credential helper into
	// the mirror so later `git fetch` calls work from launchd, where the user's
	// interactive shell config and any global helper may not be in play.
	if err := m.git(ctx, mirror, "config", "credential.https://github.com.helper", "!"+m.ghPath+" auth git-credential"); err != nil {
		return fmt.Errorf("configure credential helper for %s: %w", ev.Repo, err)
	}
	return nil
}

// clearTree removes a worktree directory and the mirror's record of it. A
// previous run killed mid-flight leaves both behind.
func (m *Manager) clearTree(ctx context.Context, mirror, path string) error {
	if _, err := os.Stat(path); err == nil {
		// Best effort: git refuses if the worktree is not registered, in which
		// case removing the directory outright is correct.
		_ = m.git(ctx, mirror, "worktree", "remove", "--force", path)
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove worktree %s: %w", path, err)
	}
	if err := m.git(ctx, mirror, "worktree", "prune"); err != nil {
		return fmt.Errorf("prune worktrees in %s: %w", mirror, err)
	}
	return nil
}

func (m *Manager) git(ctx context.Context, gitDir string, args ...string) error {
	full := append([]string{"--git-dir=" + gitDir}, args...)
	_, err := m.runner.Run(ctx, m.gitPath, full...)
	return err
}

func shortSHA(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
