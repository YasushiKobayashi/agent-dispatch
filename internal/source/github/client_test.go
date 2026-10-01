package github

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/YasushiKobayashi/agent-dispatch/internal/model"
)

// fakeRunner answers gh invocations from a table keyed by a distinctive
// substring of the argument list, and records every call it saw.
type fakeRunner struct {
	responses map[string]string
	err       error
	calls     [][]string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if f.err != nil {
		return nil, f.err
	}
	joined := strings.Join(args, " ")
	for key, body := range f.responses {
		if strings.Contains(joined, key) {
			return []byte(body), nil
		}
	}
	return nil, errors.New("fakeRunner: no response for: " + joined)
}

const searchJSON = `[
  {
    "author": {"login": "octocat"},
    "isDraft": false,
    "labels": [{"name": "backend"}, {"name": "needs-review"}],
    "number": 42,
    "repository": {"name": "widgets", "nameWithOwner": "acme/widgets"},
    "title": "feat: something",
    "url": "https://github.com/acme/widgets/pull/42"
  },
  {
    "author": {"login": "renovate[bot]"},
    "isDraft": true,
    "labels": [],
    "number": 43,
    "repository": {"name": "widgets", "nameWithOwner": "acme/widgets"},
    "title": "chore(deps): update aqua tools",
    "url": "https://github.com/acme/widgets/pull/43"
  }
]`

func TestReviewRequested(t *testing.T) {
	runner := &fakeRunner{responses: map[string]string{
		"api user":   "YasushiKobayashi\n",
		"search prs": searchJSON,
		"pr view 42": `{"headRefName":"feat/perm","headRefOid":"aaaa1111","baseRefName":"main"}`,
		"pr view 43": `{"headRefName":"renovate/aqua","headRefOid":"bbbb2222","baseRefName":"develop"}`,
	}}
	client := NewWithRunner("gh", "", runner)

	events, err := client.ReviewRequested(context.Background(), 0)
	if err != nil {
		t.Fatalf("ReviewRequested: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}

	got := events[0]
	want := model.Event{
		Kind: model.KindReviewRequested, Repo: "acme/widgets",
		Owner: "acme", Name: "widgets", PRNumber: 42,
		Author: "octocat", HeadRefName: "feat/perm", HeadSHA: "aaaa1111",
		BaseRefName: "main", Cursor: "aaaa1111",
	}
	if got.Kind != want.Kind || got.Repo != want.Repo || got.Owner != want.Owner ||
		got.Name != want.Name || got.PRNumber != want.PRNumber || got.Author != want.Author ||
		got.HeadRefName != want.HeadRefName || got.HeadSHA != want.HeadSHA ||
		got.BaseRefName != want.BaseRefName || got.Cursor != want.Cursor {
		t.Errorf("event[0] = %+v\nwant the fields of %+v", got, want)
	}
	if got.DetectedAt.IsZero() {
		t.Error("DetectedAt is zero")
	}
	if strings.Join(got.Labels, ",") != "backend,needs-review" {
		t.Errorf("labels = %v, want [backend needs-review]", got.Labels)
	}
	if !events[1].IsDraft {
		t.Error("event[1].IsDraft = false, want true")
	}
	if events[1].Cursor != "bbbb2222" {
		t.Errorf("event[1].Cursor = %q, want the PR's own head SHA", events[1].Cursor)
	}
}

func TestReviewRequestedUsesConfiguredLogin(t *testing.T) {
	runner := &fakeRunner{responses: map[string]string{"search prs": "[]"}}
	client := NewWithRunner("gh", "PinnedUser", runner)

	if _, err := client.ReviewRequested(context.Background(), 10); err != nil {
		t.Fatalf("ReviewRequested: %v", err)
	}
	for _, call := range runner.calls {
		if strings.Contains(strings.Join(call, " "), "api user") {
			t.Fatal("client called `gh api user` even though a login was configured")
		}
	}
	if !strings.Contains(strings.Join(runner.calls[0], " "), "--review-requested=PinnedUser") {
		t.Errorf("search args = %v, want --review-requested=PinnedUser", runner.calls[0])
	}
	if !strings.Contains(strings.Join(runner.calls[0], " "), "--limit 10") {
		t.Errorf("search args = %v, want --limit 10", runner.calls[0])
	}
}

func TestReviewRequestedNoHitsSkipsPRView(t *testing.T) {
	runner := &fakeRunner{responses: map[string]string{
		"api user":   "me\n",
		"search prs": "[]",
	}}

	events, err := NewWithRunner("gh", "", runner).ReviewRequested(context.Background(), 0)
	if err != nil {
		t.Fatalf("ReviewRequested: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("got %d events, want 0", len(events))
	}
	if len(runner.calls) != 2 {
		t.Errorf("made %d gh calls (%v), want exactly 2 (api user + search)", len(runner.calls), runner.calls)
	}
}

func TestReviewRequestedPropagatesGhFailure(t *testing.T) {
	runner := &fakeRunner{err: errors.New("gh: authentication required")}

	_, err := NewWithRunner("gh", "me", runner).ReviewRequested(context.Background(), 0)
	if err == nil {
		t.Fatal("ReviewRequested succeeded despite a gh failure")
	}
	if !strings.Contains(err.Error(), "authentication required") {
		t.Errorf("error = %v, want it to carry gh's stderr", err)
	}
}

func TestReviewRequestedRejectsMissingHeadSHA(t *testing.T) {
	runner := &fakeRunner{responses: map[string]string{
		"search prs": searchJSON,
		"pr view 42": `{"headRefName":"feat/perm","baseRefName":"main"}`,
	}}

	_, err := NewWithRunner("gh", "me", runner).ReviewRequested(context.Background(), 0)
	if err == nil {
		t.Fatal("ReviewRequested accepted a PR with no head SHA")
	}
	if !strings.Contains(err.Error(), "no head SHA") {
		t.Errorf("error = %v, want it to mention the missing head SHA", err)
	}
}

func TestResolveLoginRejectsEmptyOutput(t *testing.T) {
	runner := &fakeRunner{responses: map[string]string{"api user": "\n"}}

	if _, err := NewWithRunner("gh", "", runner).ResolveLogin(context.Background()); err == nil {
		t.Fatal("ResolveLogin accepted an empty login")
	}
}

func TestResolveLoginCachesResult(t *testing.T) {
	runner := &fakeRunner{responses: map[string]string{"api user": "me\n"}}
	client := NewWithRunner("gh", "", runner)

	for i := 0; i < 3; i++ {
		if _, err := client.ResolveLogin(context.Background()); err != nil {
			t.Fatalf("ResolveLogin: %v", err)
		}
	}
	if len(runner.calls) != 1 {
		t.Errorf("called gh %d times, want 1 (result should be cached)", len(runner.calls))
	}
}
