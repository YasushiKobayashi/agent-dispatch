// Package github detects pull request events by shelling out to the gh CLI, so
// reviewd inherits whatever authentication the user has already set up.
package github

import (
	"context"
	"encoding/json"
	"fmt"
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
	out, err := c.runner.Run(ctx, c.ghPath, "pr", "view", strconv.Itoa(number),
		"--repo", repo,
		"--json", "headRefName,headRefOid,baseRefName",
	)
	if err != nil {
		return prDetail{}, fmt.Errorf("view %s#%d: %w", repo, number, err)
	}

	var detail prDetail
	if err := json.Unmarshal(out, &detail); err != nil {
		return prDetail{}, fmt.Errorf("parse gh pr view output for %s#%d: %w", repo, number, err)
	}
	if detail.HeadRefOid == "" {
		return prDetail{}, fmt.Errorf("view %s#%d: gh returned no head SHA", repo, number)
	}
	return detail, nil
}

func splitRepo(nameWithOwner string) (owner, name string, ok bool) {
	owner, name, ok = strings.Cut(nameWithOwner, "/")
	if !ok || owner == "" || name == "" {
		return "", "", false
	}
	return owner, name, true
}
