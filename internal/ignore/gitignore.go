// Package ignore decides which paths under a broadcast root are shared with
// viewers. It implements gitignore pattern matching plus the always-on rules
// biga applies on top of a project's .gitignore.
package ignore

import (
	"regexp"
	"strings"
)

// Matcher evaluates gitignore patterns against slash-separated paths relative
// to the directory the patterns were loaded from. The zero value matches
// nothing; use [NewMatcher] to choose case sensitivity.
type Matcher struct {
	ignoreCase bool
	rules      []rule
}

type rule struct {
	re      *regexp.Regexp
	negate  bool
	dirOnly bool
}

// NewMatcher returns an empty matcher. When ignoreCase is true patterns match
// regardless of letter case, as git does on case-insensitive filesystems.
func NewMatcher(ignoreCase bool) *Matcher {
	return &Matcher{ignoreCase: ignoreCase}
}

// AddPatterns adds each line of a .gitignore-formatted string. Later patterns
// take precedence over earlier ones, matching git.
func (m *Matcher) AddPatterns(lines ...string) {
	for _, line := range lines {
		if r, ok := m.compile(line); ok {
			m.rules = append(m.rules, r)
		}
	}
}

// Match reports whether the slash-separated relative path is ignored. isDir
// tells the matcher whether the path names a directory, which decides whether
// directory-only patterns ("build/") apply to it.
//
// A path is also ignored when any of its parent directories is, because git
// never descends into an excluded directory to re-include its contents.
func (m *Matcher) Match(rel string, isDir bool) bool {
	if len(m.rules) == 0 || rel == "" {
		return false
	}
	for i := range len(rel) {
		if rel[i] == '/' && m.matchOne(rel[:i], true) {
			return true
		}
	}
	return m.matchOne(rel, isDir)
}

// last reports whether any rule matches rel and, if so, whether the last
// matching rule ignores it. Unlike Match it does not consider parents.
func (m *Matcher) last(rel string, isDir bool) (matched, ignored bool) {
	for i := len(m.rules) - 1; i >= 0; i-- {
		r := m.rules[i]
		if r.dirOnly && !isDir {
			continue
		}
		if r.re.MatchString(rel) {
			return true, !r.negate
		}
	}
	return false, false
}

func (m *Matcher) matchOne(rel string, isDir bool) bool {
	ignored := false
	for _, r := range m.rules {
		// Only a rule of the opposite polarity can change the outcome, so the
		// regexp is skipped for rules that would merely restate it.
		if r.negate != ignored || (r.dirOnly && !isDir) {
			continue
		}
		if r.re.MatchString(rel) {
			ignored = !r.negate
		}
	}
	return ignored
}

// compile turns one gitignore line into a rule. It reports false for blank
// lines, comments, and patterns that reduce to nothing.
func (m *Matcher) compile(line string) (rule, bool) {
	line = strings.TrimSuffix(line, "\r")
	if line == "" || line[0] == '#' {
		return rule{}, false
	}
	line = trimTrailingSpaces(line)

	var r rule
	if line[0] == '!' {
		r.negate = true
		line = line[1:]
	}
	if strings.HasSuffix(line, "/") {
		r.dirOnly = true
		line = strings.TrimRight(line, "/")
	}
	if line == "" {
		return rule{}, false
	}

	// A slash anywhere but the end anchors the pattern to the root; without
	// one it matches a name at any depth.
	anchored := strings.Contains(line, "/")
	line = strings.TrimPrefix(line, "/")

	var expr strings.Builder
	if m.ignoreCase {
		expr.WriteString("(?i)")
	}
	expr.WriteByte('^')
	if !anchored {
		expr.WriteString("(?:.*/)?")
	}
	expr.WriteString(globToRegexp(line))
	expr.WriteByte('$')

	re, err := regexp.Compile(expr.String())
	if err != nil {
		// Unbalanced or otherwise malformed globs are ignored, as git does.
		return rule{}, false
	}
	r.re = re
	return r, true
}

// trimTrailingSpaces drops unescaped trailing spaces; "foo\ " keeps its space.
func trimTrailingSpaces(s string) string {
	for strings.HasSuffix(s, " ") && !strings.HasSuffix(s, `\ `) {
		s = s[:len(s)-1]
	}
	return s
}

// globToRegexp converts a gitignore glob into a regexp body. "**" segments
// match across directories; every other wildcard stays within one segment.
func globToRegexp(pattern string) string {
	segments := strings.Split(pattern, "/")
	var b strings.Builder
	for i, seg := range segments {
		last := i == len(segments)-1
		if seg == "**" {
			if last {
				b.WriteString(".*")
			} else {
				b.WriteString("(?:.*/)?")
			}
			continue
		}
		b.WriteString(segmentToRegexp(seg))
		if !last {
			b.WriteByte('/')
		}
	}
	return b.String()
}

func segmentToRegexp(seg string) string {
	var b strings.Builder
	for i := 0; i < len(seg); i++ {
		switch c := seg[i]; c {
		case '\\':
			if i+1 < len(seg) {
				i++
				b.WriteString(regexp.QuoteMeta(seg[i : i+1]))
			} else {
				b.WriteString(`\\`)
			}
		case '*':
			b.WriteString("[^/]*")
		case '?':
			b.WriteString("[^/]")
		case '[':
			class, n := bracketToRegexp(seg[i:])
			if n == 0 {
				b.WriteString(`\[`)
				continue
			}
			b.WriteString(class)
			i += n - 1
		default:
			b.WriteString(regexp.QuoteMeta(seg[i : i+1]))
		}
	}
	return b.String()
}

// bracketToRegexp converts a leading "[...]" expression into a regexp class
// and reports how many bytes it consumed, or 0 when the bracket is unclosed.
func bracketToRegexp(s string) (string, int) {
	i := 1
	negate := i < len(s) && (s[i] == '!' || s[i] == '^')
	if negate {
		i++
	}
	start := i
	// A ']' right after the opening bracket is a literal member of the set.
	if i < len(s) && s[i] == ']' {
		i++
	}
	end := strings.IndexByte(s[i:], ']')
	if end < 0 {
		return "", 0
	}
	end += i

	var b strings.Builder
	b.WriteByte('[')
	if negate {
		// Wildcards never match a path separator, negated classes included.
		b.WriteString("^/")
	}
	for _, c := range s[start:end] {
		switch c {
		case '\\', '[', ']', '^':
			b.WriteByte('\\')
		}
		b.WriteRune(c)
	}
	b.WriteByte(']')
	return b.String(), end + 1
}
