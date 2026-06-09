// Package sqlscript splits a SQL script into individual statements, correctly
// ignoring ';' inside string literals, quoted identifiers and comments.
package sqlscript

import "strings"

// Split scans a SQL script byte-by-byte and returns each statement (including
// its leading comments/whitespace and trailing ';').
func Split(src string) []string {
	const (
		normal = iota
		sQuote
		dQuote
		backtick
		lineComment
		blockComment
	)
	var stmts []string
	var buf strings.Builder
	r := []byte(src)
	m := normal

	for i := 0; i < len(r); i++ {
		c := r[i]
		var next byte
		if i+1 < len(r) {
			next = r[i+1]
		}

		switch m {
		case normal:
			switch {
			case c == '\'':
				m = sQuote
			case c == '"':
				m = dQuote
			case c == '`':
				m = backtick
			case c == '-' && next == '-':
				m = lineComment
			case c == '#':
				m = lineComment
			case c == '/' && next == '*':
				m = blockComment
			case c == ';':
				buf.WriteByte(c)
				stmts = append(stmts, buf.String())
				buf.Reset()
				continue
			}
		case sQuote:
			if c == '\\' && i+1 < len(r) {
				buf.WriteByte(c)
				i++
				buf.WriteByte(r[i])
				continue
			}
			if c == '\'' {
				if next == '\'' {
					buf.WriteByte(c)
					i++
					buf.WriteByte(r[i])
					continue
				}
				m = normal
			}
		case dQuote:
			if c == '\\' && i+1 < len(r) {
				buf.WriteByte(c)
				i++
				buf.WriteByte(r[i])
				continue
			}
			if c == '"' {
				if next == '"' {
					buf.WriteByte(c)
					i++
					buf.WriteByte(r[i])
					continue
				}
				m = normal
			}
		case backtick:
			if c == '`' {
				if next == '`' {
					buf.WriteByte(c)
					i++
					buf.WriteByte(r[i])
					continue
				}
				m = normal
			}
		case lineComment:
			if c == '\n' {
				m = normal
			}
		case blockComment:
			if c == '*' && next == '/' {
				buf.WriteByte(c)
				i++
				buf.WriteByte(r[i])
				m = normal
				continue
			}
		}
		buf.WriteByte(c)
	}

	if strings.TrimSpace(buf.String()) != "" {
		stmts = append(stmts, buf.String())
	}
	return stmts
}

// LeadingKeyword returns the first SQL keyword of a statement (uppercased),
// skipping any leading whitespace and comments. It returns "" for a chunk that
// contains only comments/whitespace.
func LeadingKeyword(stmt string) string {
	s := stmt
	for {
		s = strings.TrimLeft(s, " \t\r\n")
		switch {
		case strings.HasPrefix(s, "--"), strings.HasPrefix(s, "#"):
			idx := strings.IndexByte(s, '\n')
			if idx < 0 {
				return ""
			}
			s = s[idx+1:]
		case strings.HasPrefix(s, "/*"):
			idx := strings.Index(s, "*/")
			if idx < 0 {
				return ""
			}
			s = s[idx+2:]
		default:
			i := 0
			for i < len(s) && isAlpha(s[i]) {
				i++
			}
			return strings.ToUpper(s[:i])
		}
	}
}

func isAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
