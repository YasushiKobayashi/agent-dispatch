package rule

import (
	"strings"
	"testing"

	"github.com/YasushiKobayashi/agent-dispatch/internal/config"
	"github.com/YasushiKobayashi/agent-dispatch/internal/model"
)

func boolPtr(b bool) *bool { return &b }

func baseEvent() model.Event {
	return model.Event{
		Kind:     model.KindReviewRequested,
		Repo:     "acme/widgets",
		Owner:    "acme",
		Name:     "widgets",
		PRNumber: 42,
		Author:   "octocat",
		Labels:   []string{"backend", "needs-review"},
	}
}

func rule(name string, m config.Match) config.Rule {
	return config.Rule{
		Name:   name,
		On:     []model.Kind{model.KindReviewRequested},
		Match:  m,
		Action: config.Action{Prompt: "review"},
	}
}

func TestMatchEmptyMatchAcceptsEverything(t *testing.T) {
	got, decisions := Match([]config.Rule{rule("all", config.Match{})}, baseEvent())
	if got == nil {
		t.Fatalf("Match returned no rule; decisions = %+v", decisions)
	}
	if got.Name != "all" {
		t.Errorf("matched %q, want %q", got.Name, "all")
	}
}

func TestMatchFirstWins(t *testing.T) {
	rules := []config.Rule{
		rule("specific", config.Match{Repos: []string{"acme/*"}}),
		rule("catch-all", config.Match{}),
	}

	got, _ := Match(rules, baseEvent())
	if got.Name != "specific" {
		t.Errorf("matched %q, want the first matching rule %q", got.Name, "specific")
	}
}

func TestMatchSkipsDisabledRules(t *testing.T) {
	disabled := rule("off", config.Match{})
	disabled.Enabled = boolPtr(false)

	got, decisions := Match([]config.Rule{disabled}, baseEvent())
	if got != nil {
		t.Fatalf("matched the disabled rule %q", got.Name)
	}
	if decisions[0].Reason != "disabled" {
		t.Errorf("reason = %q, want %q", decisions[0].Reason, "disabled")
	}
}

func TestMismatchReasons(t *testing.T) {
	cases := []struct {
		name  string
		rule  config.Rule
		event func(model.Event) model.Event
		want  string
	}{
		{
			name:  "kind not handled",
			rule:  config.Rule{Name: "r", On: []model.Kind{"something_else"}, Action: config.Action{Prompt: "x"}},
			event: func(e model.Event) model.Event { return e },
			want:  "does not handle kind",
		},
		{
			name:  "draft excluded by default",
			rule:  rule("r", config.Match{}),
			event: func(e model.Event) model.Event { e.IsDraft = true; return e },
			want:  "is a draft",
		},
		{
			name:  "repo not in allowlist",
			rule:  rule("r", config.Match{Repos: []string{"YasushiKobayashi/*"}}),
			event: func(e model.Event) model.Event { return e },
			want:  "does not match match.repos",
		},
		{
			name:  "repo excluded",
			rule:  rule("r", config.Match{ExcludeRepos: []string{"acme/**"}}),
			event: func(e model.Event) model.Event { return e },
			want:  "match.exclude_repos",
		},
		{
			name:  "author excluded",
			rule:  rule("r", config.Match{ExcludeAuthors: []string{"*[bot]"}}),
			event: func(e model.Event) model.Event { e.Author = "renovate[bot]"; return e },
			want:  "match.exclude_authors",
		},
		{
			name:  "author not in allowlist",
			rule:  rule("r", config.Match{Authors: []string{"someone-else"}}),
			event: func(e model.Event) model.Event { return e },
			want:  "does not match match.authors",
		},
		{
			name:  "label excluded",
			rule:  rule("r", config.Match{ExcludeLabels: []string{"wip"}}),
			event: func(e model.Event) model.Event { e.Labels = []string{"backend", "WIP"}; return e },
			want:  "match.exclude_labels",
		},
		{
			name:  "no label in allowlist",
			rule:  rule("r", config.Match{Labels: []string{"urgent"}}),
			event: func(e model.Event) model.Event { return e },
			want:  "no label matches match.labels",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, decisions := Match([]config.Rule{tc.rule}, tc.event(baseEvent()))
			if got != nil {
				t.Fatalf("rule matched, want it rejected")
			}
			if !strings.Contains(decisions[0].Reason, tc.want) {
				t.Errorf("reason = %q, want it to mention %q", decisions[0].Reason, tc.want)
			}
		})
	}
}

func TestMatchAllowsDraftWhenOptedIn(t *testing.T) {
	ev := baseEvent()
	ev.IsDraft = true

	got, decisions := Match([]config.Rule{rule("r", config.Match{IncludeDraft: true})}, ev)
	if got == nil {
		t.Fatalf("draft was rejected despite include_draft; decisions = %+v", decisions)
	}
}

func TestMatchExcludeWinsOverAllow(t *testing.T) {
	r := rule("r", config.Match{
		Repos:        []string{"acme/*"},
		ExcludeRepos: []string{"acme/widgets"},
	})

	got, _ := Match([]config.Rule{r}, baseEvent())
	if got != nil {
		t.Error("exclude_repos did not override repos")
	}
}
