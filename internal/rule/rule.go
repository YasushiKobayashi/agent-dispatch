// Package rule decides which rule, if any, should handle a detected event.
package rule

import (
	"fmt"

	"github.com/YasushiKobayashi/agent-dispatch/internal/config"
	"github.com/YasushiKobayashi/agent-dispatch/internal/glob"
	"github.com/YasushiKobayashi/agent-dispatch/internal/model"
)

// Decision records the verdict for one rule against one event. Reason is
// filled in for both outcomes so `reviewd poll --dry-run` can explain why a
// pull request the user expected to be picked up was not.
type Decision struct {
	Rule    string
	Matched bool
	Reason  string
}

// Match evaluates the rules against the event in order and returns the first
// one that applies, along with the decision recorded for each rule considered.
//
// First match wins, so rules should be ordered most specific first.
func Match(rules []config.Rule, ev model.Event) (*config.Rule, []Decision) {
	decisions := make([]Decision, 0, len(rules))

	for i := range rules {
		r := rules[i]
		if !r.IsEnabled() {
			decisions = append(decisions, Decision{Rule: r.Name, Reason: "disabled"})
			continue
		}
		if reason := mismatchReason(r, ev); reason != "" {
			decisions = append(decisions, Decision{Rule: r.Name, Reason: reason})
			continue
		}
		decisions = append(decisions, Decision{Rule: r.Name, Matched: true, Reason: "matched"})
		return &r, decisions
	}
	return nil, decisions
}

// mismatchReason returns the empty string when the rule applies to the event,
// and otherwise a short phrase naming the first failing condition.
//
// The exclude_* lists are checked before their allow counterparts so that an
// exclusion always wins.
func mismatchReason(r config.Rule, ev model.Event) string {
	if !r.Handles(ev.Kind) {
		return fmt.Sprintf("does not handle kind %q", ev.Kind)
	}

	m := r.Match

	if ev.IsDraft && !m.IncludeDraft {
		return "pull request is a draft (set match.include_draft to allow)"
	}

	if glob.MatchAny(m.ExcludeRepos, ev.Repo) {
		return fmt.Sprintf("repo %q is in match.exclude_repos", ev.Repo)
	}
	if len(m.Repos) > 0 && !glob.MatchAny(m.Repos, ev.Repo) {
		return fmt.Sprintf("repo %q does not match match.repos", ev.Repo)
	}

	if glob.MatchAny(m.ExcludeAuthors, ev.Author) {
		return fmt.Sprintf("author %q is in match.exclude_authors", ev.Author)
	}
	if len(m.Authors) > 0 && !glob.MatchAny(m.Authors, ev.Author) {
		return fmt.Sprintf("author %q does not match match.authors", ev.Author)
	}

	if glob.MatchAnyOf(m.ExcludeLabels, ev.Labels) {
		return "a label is in match.exclude_labels"
	}
	if len(m.Labels) > 0 && !glob.MatchAnyOf(m.Labels, ev.Labels) {
		return "no label matches match.labels"
	}

	return ""
}
