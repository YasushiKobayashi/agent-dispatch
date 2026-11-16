// Package github detects pull request events by shelling out to the gh CLI, so
// reviewd inherits whatever authentication the user has already set up.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/YasushiKobayashi/agent-dispatch/internal/execx"
	"github.com/YasushiKobayashi/agent-dispatch/internal/model"
)

// DefaultSearchLimit bounds how many open review requests a single poll looks
// at. Each hit costs one extra `gh pr view` call.
const DefaultSearchLimit = 50

// Client queries GitHub through the gh CLI.
type Client struct {
	ghPath string
	login  string
	runner execx.Runner
	now    func() time.Time
}

// New returns a Client that shells out to the gh binary at ghPath. An empty
// login means "resolve the authenticated user on first use".
func New(ghPath, login string) *Client {
	return NewWithRunner(ghPath, login, execx.Exec{})
}

// NewWithRunner is New with an injectable command runner, for tests.
func NewWithRunner(ghPath, login string, runner execx.Runner) *Client {
	if strings.TrimSpace(ghPath) == "" {
		ghPath = "gh"
	}
	return &Client{ghPath: ghPath, login: login, runner: runner, now: time.Now}
}

// ResolveLogin returns the GitHub handle whose review requests we watch,
// asking gh for the authenticated user when the config did not pin one.
func (c *Client) ResolveLogin(ctx context.Context) (string, error) {
	if strings.TrimSpace(c.login) != "" {
		return c.login, nil
	}
	out, err := c.runner.Run(ctx, c.ghPath, "api", "user", "--jq", ".login")
	if err != nil {
		return "", fmt.Errorf("resolve GitHub login: %w", err)
	}
	login := strings.TrimSpace(string(out))
	if login == "" {
		return "", fmt.Errorf("resolve GitHub login: gh returned an empty login (is `gh auth login` done?)")
	}
	c.login = login
	return login, nil
}

type searchHit struct {
	Number     int                     `json:"number"`
	Title      string                  `json:"title"`
	URL        string                  `json:"url"`
	IsDraft    bool                    `json:"isDraft"`
	Author     struct{ Login string }  `json:"author"`
	Labels     []struct{ Name string } `json:"labels"`
	Repository struct {
		Name          string `json:"name"`
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
}

type prDetail struct {
	HeadRefName string `json:"headRefName"`
	HeadRefOid  string `json:"headRefOid"`
	BaseRefName string `json:"baseRefName"`
}

// ReviewRequested returns one event per open pull request that is currently
// waiting on a review from the configured user.
//
// The cursor is the pull request's head SHA, so a PR that keeps receiving
// commits while the request is outstanding produces a fresh event each time.
func (c *Client) ReviewRequested(ctx context.Context, limit int) ([]model.Event, error) {
	login, err := c.ResolveLogin(ctx)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = DefaultSearchLimit
	}

	out, err := c.runner.Run(ctx, c.ghPath, "search", "prs",
		"--review-requested="+login,
		"--state=open",
		"--json", "number,title,url,repository,author,isDraft,labels",
		"--limit", strconv.Itoa(limit),
	)
	if err != nil {
		return nil, fmt.Errorf("search review requests for %s: %w", login, err)
	}

	var hits []searchHit
	if err := json.Unmarshal(out, &hits); err != nil {
		return nil, fmt.Errorf("parse gh search output: %w", err)
	}

	events := make([]model.Event, 0, len(hits))
	for _, hit := range hits {
		repo := hit.Repository.NameWithOwner
		owner, name, ok := splitRepo(repo)
		if !ok {
			return nil, fmt.Errorf("gh search returned an unexpected repository %q", repo)
		}

		// gh search cannot return the head SHA, so each hit costs one extra
		// call. Review requests are normally a handful, so this stays cheap.
		detail, err := c.pullRequest(ctx, repo, hit.Number)
		if err != nil {
			return nil, err
		}

		labels := make([]string, 0, len(hit.Labels))
		for _, l := range hit.Labels {
			labels = append(labels, l.Name)
		}

		events = append(events, model.Event{
			Kind:        model.KindReviewRequested,
			Repo:        repo,
			Owner:       owner,
			Name:        name,
			PRNumber:    hit.Number,
			PRTitle:     hit.Title,
			PRURL:       hit.URL,
			Author:      hit.Author.Login,
			HeadRefName: detail.HeadRefName,
			HeadSHA:     detail.HeadRefOid,
			BaseRefName: detail.BaseRefName,
			IsDraft:     hit.IsDraft,
			Labels:      labels,
			Cursor:      detail.HeadRefOid,
			DetectedAt:  c.now(),
		})
	}
	return events, nil
}

func (c *Client) pullRequest(ctx context.Context, repo string, number int) (prDetail, error) {
	var detail prDetail
	err := c.viewPR(ctx, repo, number, nil, &detail)
	return detail, err
}

// viewPR runs `gh pr view` for the head fields plus extra, decodes the result
// into v, and rejects a response with no head SHA.
func (c *Client) viewPR(ctx context.Context, repo string, number int, extra []string, v any) error {
	fields := append([]string{"headRefName", "headRefOid", "baseRefName"}, extra...)
	out, err := c.runner.Run(ctx, c.ghPath, "pr", "view", strconv.Itoa(number),
		"--repo", repo,
		"--json", strings.Join(fields, ","),
	)
	if err != nil {
		return fmt.Errorf("view %s#%d: %w", repo, number, err)
	}

	var detail prDetail
	if err := json.Unmarshal(out, &detail); err != nil {
		return fmt.Errorf("parse gh pr view output for %s#%d: %w", repo, number, err)
	}
	if detail.HeadRefOid == "" {
		return fmt.Errorf("view %s#%d: gh returned no head SHA", repo, number)
	}
	if err := json.Unmarshal(out, v); err != nil {
		return fmt.Errorf("parse gh pr view output for %s#%d: %w", repo, number, err)
	}
	return nil
}

func splitRepo(nameWithOwner string) (owner, name string, ok bool) {
	owner, name, ok = strings.Cut(nameWithOwner, "/")
	if !ok || owner == "" || name == "" {
		return "", "", false
	}
	return owner, name, true
}

type commentedHit struct {
	searchHit
	Author struct {
		Login string `json:"login"`
		IsBot bool   `json:"is_bot"`
		Type  string `json:"type"`
	} `json:"author"`
}

// isBot reports whether the pull request was opened by a bot. Their comments
// are automated output, not a person answering a review.
func (h commentedHit) isBot() bool {
	return h.Author.IsBot || strings.EqualFold(h.Author.Type, "Bot") ||
		strings.HasSuffix(strings.ToLower(h.Author.Login), "[bot]")
}

type conversation struct {
	prDetail
	Comments []struct {
		ID        string                 `json:"id"`
		Author    struct{ Login string } `json:"author"`
		Body      string                 `json:"body"`
		URL       string                 `json:"url"`
		CreatedAt time.Time              `json:"createdAt"`
	} `json:"comments"`
}

// review comes from the REST API rather than `gh pr view`, because inline
// comments refer to their review by the REST ID.
type review struct {
	ID          int64                  `json:"id"`
	User        struct{ Login string } `json:"user"`
	Body        string                 `json:"body"`
	State       string                 `json:"state"`
	HTMLURL     string                 `json:"html_url"`
	SubmittedAt time.Time              `json:"submitted_at"`
}

type inlineComment struct {
	ID       int64                  `json:"id"`
	ReviewID int64                  `json:"pull_request_review_id"`
	User     struct{ Login string } `json:"user"`
	Body     string                 `json:"body"`
	HTMLURL  string                 `json:"html_url"`
	Path     string                 `json:"path"`
	// Line is null once the comment is outdated by a later push, in which
	// case OriginalLine still names where it was written.
	Line         *int      `json:"line"`
	OriginalLine int       `json:"original_line"`
	CreatedAt    time.Time `json:"created_at"`
}

// thread is everything said on one pull request.
type thread struct {
	comments conversation
	reviews  []review
	inline   []inlineComment
}

// AuthorCommented returns one event per open pull request the configured user
// has reviewed and whose author has commented since the user last spoke on it.
//
// Only the pull request's own author counts: other reviewers and bots are not
// what this event is for. Conversation comments, inline review comments and
// non-empty review bodies are all considered.
//
// The cursor is the ID of the newest such comment, so a burst of replies fires
// once, and a further reply after the run fires again.
//
// A failure to read one pull request does not hide the others: its error is
// joined into the returned error alongside the events that were found.
func (c *Client) AuthorCommented(ctx context.Context, limit int) ([]model.Event, error) {
	login, err := c.ResolveLogin(ctx)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = DefaultSearchLimit
	}

	// A submitted review removes the review request, so the pull requests
	// waiting on an answer are found by who reviewed them, not who was asked.
	// Sorted by update so a fresh reply is never cut off by the limit.
	out, err := c.runner.Run(ctx, c.ghPath, "search", "prs",
		"--reviewed-by="+login,
		"--state=open",
		"--sort", "updated",
		"--json", "number,title,url,repository,author,isDraft,labels",
		"--limit", strconv.Itoa(limit),
	)
	if err != nil {
		return nil, fmt.Errorf("search pull requests reviewed by %s: %w", login, err)
	}

	var hits []commentedHit
	if err := json.Unmarshal(out, &hits); err != nil {
		return nil, fmt.Errorf("parse gh search output: %w", err)
	}

	var (
		events = make([]model.Event, 0, len(hits))
		errs   []error
	)
	for _, hit := range hits {
		if hit.isBot() || strings.EqualFold(hit.Author.Login, login) {
			continue
		}
		repo := hit.Repository.NameWithOwner
		owner, name, ok := splitRepo(repo)
		if !ok {
			errs = append(errs, fmt.Errorf("gh search returned an unexpected repository %q", repo))
			continue
		}

		th, err := c.thread(ctx, repo, hit.Number)
		if err != nil {
			errs = append(errs, err)
			continue
		}

		comments := authorCommentsSince(th, login, hit.Author.Login)
		if len(comments) == 0 {
			continue
		}

		labels := make([]string, 0, len(hit.Labels))
		for _, l := range hit.Labels {
			labels = append(labels, l.Name)
		}

		events = append(events, model.Event{
			Kind:        model.KindAuthorCommented,
			Repo:        repo,
			Owner:       owner,
			Name:        name,
			PRNumber:    hit.Number,
			PRTitle:     hit.Title,
			PRURL:       hit.URL,
			Author:      hit.Author.Login,
			HeadRefName: th.comments.HeadRefName,
			HeadSHA:     th.comments.HeadRefOid,
			BaseRefName: th.comments.BaseRefName,
			IsDraft:     hit.IsDraft,
			Labels:      labels,
			Comments:    comments,
			Cursor:      comments[len(comments)-1].ID,
			DetectedAt:  c.now(),
		})
	}
	return events, errors.Join(errs...)
}

// authorCommentsSince picks the author's comments posted after the reviewer
// last spoke on the pull request, oldest first.
//
// "Spoke" is a submitted review or a comment of the reviewer's own, so an
// author reply the reviewer already answered by hand does not fire. Nothing is
// returned when the reviewer has no submitted review, which keeps comments
// from before the review cycle started from firing all at once.
//
// An inline comment counts from when its review was submitted, not from when
// it was written: a reply drafted in a pending review is only published, and
// only visible, once that review is submitted.
func authorCommentsSince(th thread, reviewer, author string) []model.Comment {
	submitted := make(map[int64]time.Time, len(th.reviews))
	var reviewed, since time.Time
	for _, r := range th.reviews {
		submitted[r.ID] = r.SubmittedAt
		// A pending review is a draft only its writer can see.
		if strings.EqualFold(r.User.Login, reviewer) && r.State != "PENDING" && r.SubmittedAt.After(reviewed) {
			reviewed = r.SubmittedAt
		}
	}
	if reviewed.IsZero() {
		return nil
	}
	since = reviewed

	published := func(cm inlineComment) time.Time {
		if at, ok := submitted[cm.ReviewID]; ok && at.After(cm.CreatedAt) {
			return at
		}
		return cm.CreatedAt
	}
	for _, cm := range th.comments.Comments {
		if strings.EqualFold(cm.Author.Login, reviewer) && cm.CreatedAt.After(since) {
			since = cm.CreatedAt
		}
	}
	for _, cm := range th.inline {
		if strings.EqualFold(cm.User.Login, reviewer) && published(cm).After(since) {
			since = published(cm)
		}
	}

	var comments []model.Comment
	for _, cm := range th.comments.Comments {
		if strings.EqualFold(cm.Author.Login, author) && cm.CreatedAt.After(since) {
			comments = append(comments, model.Comment{
				ID: cm.ID, Author: cm.Author.Login, Body: cm.Body, URL: cm.URL, CreatedAt: cm.CreatedAt,
			})
		}
	}
	for _, r := range th.reviews {
		// Replying in a review thread also creates an empty review; its text
		// arrives with the inline comments instead.
		if strings.EqualFold(r.User.Login, author) && strings.TrimSpace(r.Body) != "" && r.SubmittedAt.After(since) {
			comments = append(comments, model.Comment{
				ID: strconv.FormatInt(r.ID, 10), Author: r.User.Login, Body: r.Body,
				URL: r.HTMLURL, CreatedAt: r.SubmittedAt,
			})
		}
	}
	for _, cm := range th.inline {
		if at := published(cm); strings.EqualFold(cm.User.Login, author) && at.After(since) {
			line := cm.OriginalLine
			if cm.Line != nil {
				line = *cm.Line
			}
			comments = append(comments, model.Comment{
				ID: strconv.FormatInt(cm.ID, 10), Author: cm.User.Login, Body: cm.Body,
				URL: cm.HTMLURL, Path: cm.Path, Line: line, CreatedAt: at,
			})
		}
	}

	sort.SliceStable(comments, func(i, j int) bool {
		return comments[i].CreatedAt.Before(comments[j].CreatedAt)
	})
	return comments
}

func (c *Client) thread(ctx context.Context, repo string, number int) (thread, error) {
	var th thread
	if err := c.viewPR(ctx, repo, number, []string{"comments"}, &th.comments); err != nil {
		return thread{}, err
	}
	if err := listAll(ctx, c, fmt.Sprintf("repos/%s/pulls/%d/reviews", repo, number), &th.reviews); err != nil {
		return thread{}, fmt.Errorf("list reviews on %s#%d: %w", repo, number, err)
	}
	// `gh pr view` does not expose inline comments at all.
	if err := listAll(ctx, c, fmt.Sprintf("repos/%s/pulls/%d/comments", repo, number), &th.inline); err != nil {
		return thread{}, fmt.Errorf("list inline comments on %s#%d: %w", repo, number, err)
	}
	return th, nil
}

// listAll reads every page of a REST list endpoint into dst.
func listAll[T any](ctx context.Context, c *Client, path string, dst *[]T) error {
	// --jq '.[]' emits one object per line across every page; a bare
	// --paginate would print one JSON array per page back to back.
	out, err := c.runner.Run(ctx, c.ghPath, "api", path, "--paginate", "--jq", ".[]")
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var item T
		if err := dec.Decode(&item); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		*dst = append(*dst, item)
	}
	return nil
}
