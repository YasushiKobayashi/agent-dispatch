// Package glob implements the small pattern syntax used by rules.yaml for
// repository, author and label matching.
//
// The syntax is deliberately narrow:
//
//   - matches any run of characters except "/"
//     **  matches any run of characters, including "/"
//     ?   matches exactly one character except "/"
//
// Matching is case-insensitive, because GitHub logins, repository names and
// labels are. Every other character is literal, so there is no such thing as an
// invalid pattern and compiling never fails.
package glob

import (
	"regexp"
	"strings"
)

// Pattern is a compiled glob.
type Pattern struct {
	src string
	re  *regexp.Regexp
}

// Compile turns a glob into a Pattern.
//
// It cannot fail: every character outside the three metacharacters above is
// passed through regexp.QuoteMeta, so the generated expression is valid by
// construction.
func Compile(pattern string) *Pattern {
	var sb strings.Builder
	sb.WriteString("(?is)^")

	runes := []rune(pattern)
	for i := 0; i < len(runes); i++ {
		switch runes[i] {
		case '*':
			if i+1 < len(runes) && runes[i+1] == '*' {
				sb.WriteString(".*")
				i++
				continue
			}
			sb.WriteString("[^/]*")
		case '?':
			sb.WriteString("[^/]")
		default:
			sb.WriteString(regexp.QuoteMeta(string(runes[i])))
		}
	}
	sb.WriteString("$")

	return &Pattern{src: pattern, re: regexp.MustCompile(sb.String())}
}

// String returns the original pattern text.
func (p *Pattern) String() string { return p.src }

// Match reports whether s matches the pattern.
func (p *Pattern) Match(s string) bool { return p.re.MatchString(s) }

// MatchAny reports whether s matches at least one of the patterns. An empty
// pattern list never matches; callers decide what "no constraint" means.
func MatchAny(patterns []string, s string) bool {
	for _, pattern := range patterns {
		if Compile(pattern).Match(s) {
			return true
		}
	}
	return false
}

// MatchAnyOf reports whether any of the candidates matches any of the patterns.
func MatchAnyOf(patterns, candidates []string) bool {
	for _, candidate := range candidates {
		if MatchAny(patterns, candidate) {
			return true
		}
	}
	return false
}
