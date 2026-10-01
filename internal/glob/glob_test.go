package glob

import "testing"

func TestMatch(t *testing.T) {
	cases := []struct {
		pattern string
		input   string
		want    bool
	}{
		{"YasushiKobayashi/*", "YasushiKobayashi/agent-dispatch", true},
		{"YasushiKobayashi/*", "YasushiKobayashi/a/b", false},
		{"YasushiKobayashi/**", "YasushiKobayashi/a/b", true},
		{"**", "acme/widgets", true},
		{"*", "acme/widgets", false},
		{"*", "renovate[bot]", true},
		{"*[bot]", "renovate[bot]", true},
		{"*[bot]", "octocat", false},
		{"acme/widgets", "ACME/Widgets", true},
		{"wip", "WIP", true},
		{"needs-?eview", "needs-review", true},
		{"needs-?eview", "needs-eview", false},
		{"a.b", "axb", false},
		{"", "", true},
		{"", "x", false},
	}
	for _, tc := range cases {
		if got := Compile(tc.pattern).Match(tc.input); got != tc.want {
			t.Errorf("Compile(%q).Match(%q) = %v, want %v", tc.pattern, tc.input, got, tc.want)
		}
	}
}

// Compile is documented as infallible. Anything a user can type into
// rules.yaml must therefore survive it without panicking.
func TestCompileNeverPanics(t *testing.T) {
	nasty := []string{
		"[", "]", "(", ")", "\\", "\\\\", "+", "^", "$", "|", "{2,}",
		"***", "*?*", "a**b*c", "**/**", "(?i)", "[a-", "日本語/*", "\x00",
	}
	for _, pattern := range nasty {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Compile(%q) panicked: %v", pattern, r)
				}
			}()
			Compile(pattern).Match("anything")
		}()
	}
}

func TestCompileTreatsRegexMetacharactersAsLiterals(t *testing.T) {
	if Compile("a+").Match("aaa") {
		t.Error(`Compile("a+") matched "aaa"; + must be literal`)
	}
	if !Compile("a+").Match("a+") {
		t.Error(`Compile("a+") did not match "a+"`)
	}
}

func TestMatchAnyEmptyNeverMatches(t *testing.T) {
	if MatchAny(nil, "anything") {
		t.Error("MatchAny(nil, ...) = true, want false")
	}
}

func TestMatchAnyOf(t *testing.T) {
	if !MatchAnyOf([]string{"wip", "do-not-review"}, []string{"backend", "WIP"}) {
		t.Error("MatchAnyOf did not match a label differing only by case")
	}
	if MatchAnyOf([]string{"wip"}, []string{"backend"}) {
		t.Error("MatchAnyOf matched an unrelated label")
	}
	if MatchAnyOf([]string{"wip"}, nil) {
		t.Error("MatchAnyOf matched with no candidates")
	}
}
