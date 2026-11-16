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

const reviewedJSON = `[
  {
    "author": {"login": "octocat", "is_bot": false, "type": "User"},
    "isDraft": false,
    "labels": [{"name": "backend"}],
    "number": 42,
    "repository": {"name": "widgets", "nameWithOwner": "acme/widgets"},
    "title": "feat: something",
    "url": "https://github.com/acme/widgets/pull/42"
  },
  {
    "author": {"login": "app/renovate", "is_bot": true, "type": "Bot"},
    "isDraft": false,
    "labels": [],
    "number": 43,
    "repository": {"name": "widgets", "nameWithOwner": "acme/widgets"},
    "title": "chore(deps): update",
    "url": "https://github.com/acme/widgets/pull/43"
  },
  {
    "author": {"login": "me", "is_bot": false, "type": "User"},
    "isDraft": false,
    "labels": [],
    "number": 44,
    "repository": {"name": "widgets", "nameWithOwner": "acme/widgets"},
    "title": "feat: mine",
    "url": "https://github.com/acme/widgets/pull/44"
  }
]`

// conversation42 has a review from "me" at 10:00. The author answered after it
// in the conversation, in a review body and inline; everything else is either
// before the review or from someone other than the author.
const conversation42 = `{
  "headRefName": "feat/perm", "headRefOid": "aaaa1111", "baseRefName": "main",
  "comments": [
    {"id": "IC_old", "author": {"login": "octocat"}, "body": "before review", "url": "u0", "createdAt": "2026-11-01T09:00:00Z"},
    {"id": "IC_new", "author": {"login": "octocat"}, "body": "fixed, please look again", "url": "u1", "createdAt": "2026-11-01T11:00:00Z"},
    {"id": "IC_other", "author": {"login": "someone"}, "body": "drive-by", "url": "u2", "createdAt": "2026-11-01T11:30:00Z"},
    {"id": "IC_bot", "author": {"login": "github-actions"}, "body": "CI passed", "url": "u3", "createdAt": "2026-11-01T11:40:00Z"}
  ],
  "reviews": [
    {"id": "PRR_me", "author": {"login": "me"}, "body": "please fix", "state": "CHANGES_REQUESTED", "submittedAt": "2026-11-01T10:00:00Z"},
    {"id": "PRR_pending", "author": {"login": "me"}, "body": "", "state": "PENDING", "submittedAt": "0001-01-01T00:00:00Z"},
    {"id": "PRR_thread", "author": {"login": "octocat"}, "body": "", "state": "COMMENTED", "submittedAt": "2026-11-01T12:00:00Z"},
    {"id": "PRR_body", "author": {"login": "octocat"}, "body": "summary of changes", "state": "COMMENTED", "submittedAt": "2026-11-01T12:30:00Z"}
  ]
}`

const inline42 = `{"id": 7, "user": {"login": "octocat"}, "body": "done", "html_url": "u7", "path": "a.go", "line": 12, "created_at": "2026-11-01T12:00:00Z"}
{"id": 5, "user": {"login": "me"}, "body": "nit", "html_url": "u5", "path": "a.go", "line": 12, "created_at": "2026-11-01T10:00:00Z"}
`

func TestAuthorCommented(t *testing.T) {
	runner := &fakeRunner{responses: map[string]string{
		"search prs":        reviewedJSON,
		"pr view 42":        conversation42,
		"pulls/42/comments": inline42,
	}}

	events, err := NewWithRunner("gh", "me", runner).AuthorCommented(context.Background(), 0)
	if err != nil {
		t.Fatalf("AuthorCommented: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 (bot PR and own PR are skipped)", len(events))
	}

	ev := events[0]
	if ev.Kind != model.KindAuthorCommented || ev.Repo != "acme/widgets" || ev.PRNumber != 42 ||
		ev.Author != "octocat" || ev.HeadSHA != "aaaa1111" || ev.BaseRefName != "main" {
		t.Errorf("event = %+v", ev)
	}

	var ids []string
	for _, c := range ev.Comments {
		ids = append(ids, c.ID)
	}
	if got, want := strings.Join(ids, ","), "IC_new,7,PRR_body"; got != want {
		t.Errorf("comments = %s, want %s (author only, after my review, oldest first)", got, want)
	}
	if ev.Cursor != "PRR_body" {
		t.Errorf("cursor = %q, want the newest comment's ID", ev.Cursor)
	}
	if c := ev.Comments[1]; c.Path != "a.go" || c.Line != 12 || c.URL != "u7" {
		t.Errorf("inline comment = %+v, want path/line/url carried over", c)
	}

	if !strings.Contains(strings.Join(runner.calls[0], " "), "--reviewed-by=me") {
		t.Errorf("search args = %v, want --reviewed-by=me", runner.calls[0])
	}
	for _, call := range runner.calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "43") || strings.Contains(joined, "44") {
			t.Errorf("looked up a skipped PR: %s", joined)
		}
	}
}

func TestAuthorCommentedIgnoresPRsWithoutNewAuthorComments(t *testing.T) {
	runner := &fakeRunner{responses: map[string]string{
		"search prs": reviewedJSON,
		"pr view 42": `{"headRefName":"f","headRefOid":"aaaa1111","baseRefName":"main",
		  "comments":[{"id":"IC_old","author":{"login":"octocat"},"body":"x","createdAt":"2026-11-01T09:00:00Z"}],
		  "reviews":[{"id":"PRR_me","author":{"login":"me"},"state":"APPROVED","submittedAt":"2026-11-01T10:00:00Z"}]}`,
		"pulls/42/comments": "",
	}}

	events, err := NewWithRunner("gh", "me", runner).AuthorCommented(context.Background(), 0)
	if err != nil {
		t.Fatalf("AuthorCommented: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("got %d events, want 0: the only author comment predates my review", len(events))
	}
}

func TestAuthorCommentedPropagatesGhFailure(t *testing.T) {
	runner := &fakeRunner{err: errors.New("gh: authentication required")}

	if _, err := NewWithRunner("gh", "me", runner).AuthorCommented(context.Background(), 0); err == nil {
		t.Fatal("AuthorCommented succeeded despite a gh failure")
	}
}
