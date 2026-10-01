// Package model holds the types shared across reviewd's packages.
package model

import (
	"fmt"
	"time"
)

// Kind identifies what happened on GitHub that made us want to run Claude Code.
type Kind string

const (
	// KindReviewRequested fires when a review has been requested from the
	// configured GitHub user on an open pull request.
	KindReviewRequested Kind = "review_requested"
)

// AllKinds lists every kind reviewd can currently detect. Rules are validated
// against it so a typo in rules.yaml fails at load time rather than silently
// never matching.
var AllKinds = []Kind{KindReviewRequested}

// Valid reports whether k is a kind reviewd knows how to detect.
func (k Kind) Valid() bool {
	for _, known := range AllKinds {
		if k == known {
			return true
		}
	}
	return false
}

func (k Kind) String() string { return string(k) }

// Event is a single detected occurrence on a pull request. Cursor carries the
// value used for idempotency: two events with the same Repo, PRNumber, Kind and
// Cursor are the same event and must only be acted on once.
type Event struct {
	Kind Kind

	Repo     string // "owner/name"
	Owner    string
	Name     string
	PRNumber int

	PRTitle string
	PRURL   string
	Author  string

	HeadRefName string
	HeadSHA     string
	BaseRefName string
	IsDraft     bool
	Labels      []string

	Cursor     string
	DetectedAt time.Time
}

// Key is a human readable identifier for the event, used in logs and lock names.
func (e Event) Key() string {
	return fmt.Sprintf("%s#%d:%s:%s", e.Repo, e.PRNumber, e.Kind, shortCursor(e.Cursor))
}

// Slug is a filesystem safe form of the repository name, e.g. "owner__name".
func (e Event) Slug() string {
	return fmt.Sprintf("%s__%s", e.Owner, e.Name)
}

// TreeName is the directory name of the event's worktree, e.g. "widgets-pr42".
// A terminal tab shows the last path component as its title, so this is what
// tells one open review apart from another at a glance.
func (e Event) TreeName() string {
	return fmt.Sprintf("%s-pr%d", e.Name, e.PRNumber)
}

func shortCursor(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

// RunStatus is the terminal (or in-progress) state of a Claude Code run.
type RunStatus string

const (
	RunRunning RunStatus = "running"
	RunSuccess RunStatus = "success"
	RunFailed  RunStatus = "failed"
	RunTimeout RunStatus = "timeout"
	RunSkipped RunStatus = "skipped"
	// RunLaunched means Claude Code was handed to an interactive terminal tab.
	// It is terminal from reviewd's point of view: we started the session and
	// will never learn how it ended, because reading the answer is the user's
	// job in that mode.
	RunLaunched RunStatus = "launched"
)

// Run records one attempt at running Claude Code for an event.
type Run struct {
	ID         int64
	Repo       string
	PRNumber   int
	Kind       Kind
	Cursor     string
	Rule       string
	Status     RunStatus
	StartedAt  time.Time
	FinishedAt *time.Time
	ExitCode   *int
	OutputDir  string
	Error      string
}
