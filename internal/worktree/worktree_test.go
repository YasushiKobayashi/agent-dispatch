package worktree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/YasushiKobayashi/agent-dispatch/internal/execx"
	"github.com/YasushiKobayashi/agent-dispatch/internal/model"
)

type recorder struct {
	calls  [][]string
	failOn string
	// onClone, when set, creates the bare mirror the way gh would.
	onClone func(dir string) error
}

func (r *recorder) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	joined := strings.Join(append([]string{name}, args...), " ")
	r.calls = append(r.calls, append([]string{name}, args...))

	if r.failOn != "" && strings.Contains(joined, r.failOn) {
		return nil, errors.New("boom: " + r.failOn)
	}
	if name == "gh" && len(args) >= 2 && args[0] == "repo" && args[1] == "clone" && r.onClone != nil {
		if err := r.onClone(args[3]); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func (r *recorder) find(substr string) []string {
	for _, call := range r.calls {
		if strings.Contains(strings.Join(call, " "), substr) {
			return call
		}
	}
	return nil
}

func (r *recorder) count(substr string) int {
	n := 0
	for _, call := range r.calls {
		if strings.Contains(strings.Join(call, " "), substr) {
			n++
		}
	}
	return n
}

func event() model.Event {
	return model.Event{
		Kind: model.KindReviewRequested, Repo: "acme/widgets",
		Owner: "acme", Name: "widgets", PRNumber: 42,
		HeadSHA: "aaaa1111bbbb2222", HeadRefName: "feat/perm", BaseRefName: "main",
	}
}

func newManager(t *testing.T, r execx.Runner) (*Manager, string) {
	t.Helper()
	root := t.TempDir()
	return NewWithRunner(filepath.Join(root, "repos"), filepath.Join(root, "trees"), "gh", r), root
}

func writeHEAD(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644)
}

func TestPrepareClonesFetchesAndAddsWorktree(t *testing.T) {
	r := &recorder{onClone: writeHEAD}
	m, root := newManager(t, r)

	tree, err := m.Prepare(context.Background(), event())
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	wantPath := filepath.Join(root, "trees", "acme__widgets", "widgets-pr42")
	if tree.Path != wantPath {
		t.Errorf("tree.Path = %q, want %q", tree.Path, wantPath)
	}
	if tree.MirrorPath != filepath.Join(root, "repos", "acme__widgets.git") {
		t.Errorf("tree.MirrorPath = %q", tree.MirrorPath)
	}
	if tree.BaseRef != "refs/remotes/origin/main" {
		t.Errorf("tree.BaseRef = %q, want refs/remotes/origin/main", tree.BaseRef)
	}

	clone := r.find("repo clone")
	if clone == nil {
		t.Fatal("gh repo clone was never run")
	}
	if clone[len(clone)-1] != "--bare" {
		t.Errorf("clone args = %v, want it to end with --bare", clone)
	}

	// A fork's head commit is only reachable through refs/pull/<n>/head.
	fetch := r.find("fetch")
	if fetch == nil {
		t.Fatal("git fetch was never run")
	}
	joined := strings.Join(fetch, " ")
	if !strings.Contains(joined, "+refs/pull/42/head:refs/reviewd/pr/42") {
		t.Errorf("fetch args = %v, want the pull ref refspec", fetch)
	}
	if !strings.Contains(joined, "+refs/heads/main:refs/remotes/origin/main") {
		t.Errorf("fetch args = %v, want the base branch refspec", fetch)
	}

	add := r.find("worktree add")
	if add == nil {
		t.Fatal("git worktree add was never run")
	}
	if add[len(add)-1] != "aaaa1111bbbb2222" {
		t.Errorf("worktree add checked out %q, want the event's head SHA", add[len(add)-1])
	}
	if !strings.Contains(strings.Join(add, " "), "--detach") {
		t.Errorf("worktree add args = %v, want --detach", add)
	}
}

func TestPreparePinsCredentialHelperOnFirstCloneOnly(t *testing.T) {
	r := &recorder{onClone: writeHEAD}
	m, _ := newManager(t, r)
	ctx := context.Background()

	if _, err := m.Prepare(ctx, event()); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	helper := r.find("credential.https://github.com.helper")
	if helper == nil {
		t.Fatal("credential helper was not configured on the mirror")
	}
	if helper[len(helper)-1] != "!gh auth git-credential" {
		t.Errorf("helper = %q, want %q", helper[len(helper)-1], "!gh auth git-credential")
	}

	if _, err := m.Prepare(ctx, event()); err != nil {
		t.Fatalf("Prepare (second): %v", err)
	}
	if got := r.count("repo clone"); got != 1 {
		t.Errorf("cloned %d times, want the mirror to be reused", got)
	}
}

func TestPrepareClearsAStaleWorktreeDirectory(t *testing.T) {
	r := &recorder{onClone: writeHEAD}
	m, root := newManager(t, r)

	stale := filepath.Join(root, "trees", "acme__widgets", "widgets-pr42")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	leftover := filepath.Join(stale, "leftover.txt")
	if err := os.WriteFile(leftover, []byte("from a killed run"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := m.Prepare(context.Background(), event()); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Errorf("stale worktree content survived Prepare (stat err = %v)", err)
	}
	if r.find("worktree prune") == nil {
		t.Error("git worktree prune was never run")
	}
}

func TestPrepareSkipsBaseRefspecWhenBaseUnknown(t *testing.T) {
	r := &recorder{onClone: writeHEAD}
	m, _ := newManager(t, r)

	ev := event()
	ev.BaseRefName = ""
	tree, err := m.Prepare(context.Background(), ev)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if tree.BaseRef != "" {
		t.Errorf("BaseRef = %q, want empty when the base branch is unknown", tree.BaseRef)
	}
	if strings.Contains(strings.Join(r.find("fetch"), " "), "refs/remotes/origin/") {
		t.Error("fetched a base refspec despite an unknown base branch")
	}
}

func TestPrepareFallsBackToPullRefWithoutHeadSHA(t *testing.T) {
	r := &recorder{onClone: writeHEAD}
	m, _ := newManager(t, r)

	ev := event()
	ev.HeadSHA = ""
	if _, err := m.Prepare(context.Background(), ev); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	add := r.find("worktree add")
	if add[len(add)-1] != "refs/reviewd/pr/42" {
		t.Errorf("checked out %q, want the fetched pull ref", add[len(add)-1])
	}
}

func TestPrepareReportsFetchFailure(t *testing.T) {
	r := &recorder{onClone: writeHEAD, failOn: "fetch"}
	m, _ := newManager(t, r)

	_, err := m.Prepare(context.Background(), event())
	if err == nil {
		t.Fatal("Prepare succeeded despite a failing fetch")
	}
	if !strings.Contains(err.Error(), "acme/widgets#42") {
		t.Errorf("error = %v, want it to name the pull request", err)
	}
}

func TestRemoveIsSafeOnZeroValue(t *testing.T) {
	var tree *Tree
	if err := tree.Remove(context.Background()); err != nil {
		t.Errorf("Remove on nil tree = %v, want nil", err)
	}
	if err := (&Tree{}).Remove(context.Background()); err != nil {
		t.Errorf("Remove on zero tree = %v, want nil", err)
	}
}

func TestRemoveDeletesTheDirectory(t *testing.T) {
	r := &recorder{onClone: writeHEAD}
	m, _ := newManager(t, r)
	ctx := context.Background()

	tree, err := m.Prepare(ctx, event())
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	// The fake runner never actually creates the checkout, so stand in for git.
	if err := os.MkdirAll(tree.Path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := tree.Remove(ctx); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(tree.Path); !os.IsNotExist(err) {
		t.Errorf("worktree directory survived Remove (stat err = %v)", err)
	}
}

// A terminal tab titles itself after the directory it sits in, so the leaf has
// to name the repository as well as the pull request.
func TestTreePathLeafIdentifiesTheRepositoryAndPR(t *testing.T) {
	m := New(t.TempDir(), t.TempDir(), "gh")

	got := filepath.Base(m.TreePath(model.Event{
		Owner: "acme", Name: "widgets", PRNumber: 42,
	}))
	if got != "widgets-pr42" {
		t.Fatalf("leaf = %q, want %q", got, "widgets-pr42")
	}
}

// Two repositories that share a name must not share a worktree.
func TestTreePathSeparatesRepositoriesWithTheSameName(t *testing.T) {
	m := New(t.TempDir(), t.TempDir(), "gh")

	a := m.TreePath(model.Event{Owner: "acme", Name: "web", PRNumber: 1})
	b := m.TreePath(model.Event{Owner: "reno-shelter", Name: "web", PRNumber: 1})
	if a == b {
		t.Fatalf("both owners map to %q", a)
	}
}
