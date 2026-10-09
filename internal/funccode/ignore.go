package funccode

import (
	"regexp"
	"strings"
)

// Ignore is a .gitignore, read as git reads one at the root of a repository:
// what it says of a path, the last pattern that matches has.
type Ignore struct {
	rules []ignoreRule
}

type ignoreRule struct {
	re      *regexp.Regexp
	negate  bool
	dirOnly bool
}

// ParseIgnore reads a .gitignore's text. A pattern it cannot read is left out.
func ParseIgnore(text string) *Ignore {
	ig := &Ignore{}
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimRight(line, " \t\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var r ignoreRule
		if strings.HasPrefix(line, "!") {
			r.negate, line = true, line[1:]
		} else if strings.HasPrefix(line, `\#`) || strings.HasPrefix(line, `\!`) {
			line = line[1:] // the characters themselves; other escapes are the glob's
		}
		if strings.HasSuffix(line, "/") {
			r.dirOnly, line = true, strings.TrimRight(line, "/")
		}
		// A slash at its start or in its middle anchors a pattern to the root.
		anchored := strings.Contains(line, "/")
		line = strings.TrimPrefix(line, "/")
		if line == "" {
			continue
		}
		expr := globRegexp(line)
		if !anchored {
			expr = "(?:.*/)?" + expr
		}
		re, err := regexp.Compile("^" + expr + "$")
		if err != nil {
			continue
		}
		r.re = re
		ig.rules = append(ig.rules, r)
	}
	return ig
}

// Match says whether path - relative, with / between its parts - is ignored;
// dir says it is a directory.
func (ig *Ignore) Match(path string, dir bool) bool {
	if ig == nil {
		return false
	}
	ignored := false
	for _, r := range ig.rules {
		if r.dirOnly && !dir {
			continue
		}
		if r.re.MatchString(path) {
			ignored = !r.negate
		}
	}
	return ignored
}

// globRegexp is a gitignore pattern as a regular expression: * and ? within a
// part, [...] a class, ** any number of parts.
func globRegexp(glob string) string {
	var b strings.Builder
	for i := 0; i < len(glob); i++ {
		switch c := glob[i]; c {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				switch {
				case i+2 < len(glob) && glob[i+2] == '/': // **/: any parts, or none
					b.WriteString("(?:.*/)?")
					i += 2
				default: // a trailing ** - or one inside a part - is everything below
					b.WriteString(".*")
					i++
				}
				continue
			}
			b.WriteString("[^/]*")
		case '?':
			b.WriteString("[^/]")
		case '[':
			end := strings.IndexByte(glob[i+1:], ']')
			if end < 0 {
				b.WriteString(`\[`)
				continue
			}
			class := glob[i+1 : i+1+end]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			b.WriteString("[" + strings.ReplaceAll(class, `\`, `\\`) + "]")
			i += end + 1
		case '\\':
			if i+1 < len(glob) {
				i++
				b.WriteString(regexp.QuoteMeta(string(glob[i])))
			}
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return b.String()
}
