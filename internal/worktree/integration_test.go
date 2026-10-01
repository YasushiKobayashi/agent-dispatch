package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/YasushiKobayashi/agent-dispatch/internal/execx"
	"github.com/YasushiKobayashi/agent-dispatch/internal/model"
)

// TestPrepareAgainstRealGit exercises the actual git invocations. The unit
// tests above only assert on the arguments we pass; this one proves git
// accepts them, which is where refspec and bare-worktree mistakes show up.
//
// No network is involved: "origin" is a local repository on disk.
func TestPrepareAgainstRealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	root := t.TempDir()
	origin := filepath.Join(root, "origin")
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=reviewd", "GIT_AUTHOR_EMAIL=reviewd@example.com",
			"GIT_COMMITTER_NAME=reviewd", "GIT_COMMITTER_EMAIL=reviewd@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}

	// A repository with a main branch and a PR branch, plus the refs/pull ref
	// GitHub would publish for the pull request.
	if err := os.MkdirAll(origin, 0o755); err != nil {
		t.Fatal(err)
	}
	git(origin, "init", "--initial-branch=main", ".")
	if err := os.WriteFile(filepath.Join(origin, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(origin, "add", "README.md")
	git(origin, "commit", "-m", "base")

	git(origin, "checkout", "-b", "feat/perm")
	if err := os.WriteFile(filepath.Join(origin, "feature.txt"), []byte("pr content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(origin, "add", "feature.txt")
	git(origin, "commit", "-m", "feature")
	headSHA := git(origin, "rev-parse", "HEAD")
	git(origin, "update-ref", "refs/pull/7/head", headSHA)
	git(origin, "checkout", "main")

	// Stand in for `gh repo clone --bare`, which would need the network.
	reposDir := filepath.Join(root, "repos")
	treesDir := filepath.Join(root, "trees")
	mirror := filepath.Join(reposDir, "acme__widgets.git")
	if err := os.MkdirAll(reposDir, 0o755); err != nil {
		t.Fatal(err)
	}
	git(root, "clone", "--bare", origin, mirror)

	m := NewWithRunner(reposDir, treesDir, "gh", execx.Exec{})
	ev := model.Event{
		Kind: model.KindReviewRequested, Repo: "acme/widgets",
		Owner: "acme", Name: "widgets", PRNumber: 7,
		HeadSHA: headSHA, HeadRefName: "feat/perm", BaseRefName: "main",
	}

	ctx := context.Background()
	tree, err := m.Prepare(ctx, ev)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	if got := git(tree.Path, "rev-parse", "HEAD"); got != headSHA {
		t.Errorf("worktree HEAD = %s, want the event's head SHA %s", got, headSHA)
	}
	if _, err := os.Stat(filepath.Join(tree.Path, "feature.txt")); err != nil {
		t.Errorf("worktree is missing the PR's file: %v", err)
	}
	// The base ref must be usable for a three-dot diff from inside the tree.
	if out := git(tree.Path, "diff", "--name-only", tree.BaseRef+"...HEAD"); out != "feature.txt" {
		t.Errorf("diff against %s = %q, want %q", tree.BaseRef, out, "feature.txt")
	}

	// Preparing twice must work: the second run replaces the first worktree.
	if _, err := m.Prepare(ctx, ev); err != nil {
		t.Fatalf("Prepare (second): %v", err)
	}
	if _, err := os.Stat(filepath.Join(tree.Path, "feature.txt")); err != nil {
		t.Errorf("worktree missing after re-preparing: %v", err)
	}

	if err := tree.Remove(ctx); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(tree.Path); !os.IsNotExist(err) {
		t.Errorf("worktree survived Remove (stat err = %v)", err)
	}
}
