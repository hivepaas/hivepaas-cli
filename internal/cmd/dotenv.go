package cmd

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// dotenvKey is a variable's name as a .env file writes it.
var dotenvKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)

// parseDotenv reads a .env file: KEY=VALUE lines, with comments, blank lines and
// an export before the key allowed. A value is bare - up to a comment after a
// space - or 'single-quoted', taken as it is, or "double-quoted", with \n, \t,
// \" and \\ read as escapes; quoted values may span lines. A key written twice
// takes the last value.
func parseDotenv(content string) ([][2]string, error) {
	var pairs [][2]string
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		lineNo := i + 1
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, rest, found := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !found || !dotenvKey.MatchString(key) {
			return nil, fmt.Errorf("line %d is not KEY=VALUE", lineNo) //nolint:err113
		}
		rest = strings.TrimLeft(rest, " \t")
		var value string
		switch {
		case strings.HasPrefix(rest, `"`) || strings.HasPrefix(rest, "'"):
			var used int
			var err error
			if value, used, err = quotedValue(rest, lines[i+1:]); err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNo, err)
			}
			i += used
		default:
			value = rest
			if j := strings.Index(value, " #"); j >= 0 {
				value = value[:j]
			}
			value = strings.TrimSpace(value)
		}
		pairs = appendPair(pairs, key, value)
	}
	return pairs, nil
}

// quotedValue is the value rest starts with the quote of, which runs to the
// closing quote, over as many of more as it takes; used is how many of more.
func quotedValue(rest string, more []string) (value string, used int, err error) {
	quote, body := rest[:1], rest[1:]
	for {
		if end := closingQuote(body, quote); end >= 0 {
			if after := strings.TrimSpace(body[end+1:]); after != "" && !strings.HasPrefix(after, "#") {
				return "", 0, errors.New("something after the closing quote") //nolint:err113
			}
			value = body[:end]
			if quote == `"` {
				value = unescape(value)
			}
			return value, used, nil
		}
		if used == len(more) {
			return "", 0, errors.New("the quote is not closed") //nolint:err113
		}
		body += "\n" + more[used]
		used++
	}
}

// closingQuote is where quote closes s: in double quotes, after any escapes.
func closingQuote(s, quote string) int {
	for i := 0; i < len(s); i++ {
		switch {
		case quote == `"` && s[i] == '\\':
			i++
		case s[i:i+1] == quote:
			return i
		}
	}
	return -1
}

func unescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		default: // \" \\ and anything else: the character itself
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

func appendPair(pairs [][2]string, key, value string) [][2]string {
	for i := range pairs {
		if pairs[i][0] == key {
			pairs[i][1] = value
			return pairs
		}
	}
	return append(pairs, [2]string{key, value})
}

// bareValue is a value a .env file can write without quotes.
var bareValue = regexp.MustCompile(`^[A-Za-z0-9_./:@,+=-]*$`)

// formatDotenv writes pairs as a .env file reads them back: a plain value as it
// is, others in single quotes, which no loader expands, and a value with a
// single quote or a line break in double quotes, escaped.
func formatDotenv(pairs [][2]string) string {
	var b strings.Builder
	for _, pair := range pairs {
		key, value := pair[0], pair[1]
		switch {
		case bareValue.MatchString(value):
		case !strings.ContainsAny(value, "'\n\r"):
			value = "'" + value + "'"
		default:
			value = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`).Replace(value) + `"`
		}
		fmt.Fprintf(&b, "%s=%s\n", key, value)
	}
	return b.String()
}
