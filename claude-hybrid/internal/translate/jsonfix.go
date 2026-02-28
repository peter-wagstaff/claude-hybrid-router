package translate

import (
	"encoding/json"
	"regexp"
	"strings"
)

var (
	trailingCommaObj = regexp.MustCompile(`,\s*}`)
	trailingCommaArr = regexp.MustCompile(`,\s*]`)
)

// FixJSON attempts to repair malformed JSON from LLM output.
// It uses a 3-tier approach: standard parse, relaxed fixups, bracket repair.
// Falls back to "{}" if nothing works.
func FixJSON(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "{}"
	}

	// Tier 1: already valid
	if json.Valid([]byte(s)) {
		return s
	}

	// Tier 2: relaxed fixups
	fixed := relaxedFix(s)
	if json.Valid([]byte(fixed)) {
		return fixed
	}

	// Tier 3: close unclosed brackets
	fixed = closeBrackets(fixed)
	if json.Valid([]byte(fixed)) {
		return fixed
	}

	return "{}"
}

// relaxedFix applies common repairs: trailing commas, single quotes, unquoted keys.
func relaxedFix(s string) string {
	// Strip trailing commas before } and ]
	s = trailingCommaObj.ReplaceAllString(s, "}")
	s = trailingCommaArr.ReplaceAllString(s, "]")

	// Replace single-quoted strings with double-quoted.
	// Walk character by character to handle this correctly.
	s = replaceSingleQuotes(s)

	return s
}

// replaceSingleQuotes converts single-quoted strings to double-quoted.
func replaceSingleQuotes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inDouble := false
	inSingle := false

	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) && (inDouble || inSingle) {
			// Escaped character inside a string — pass through both chars.
			b.WriteByte(c)
			i++
			b.WriteByte(s[i])
			continue
		}
		switch {
		case c == '"' && !inSingle:
			inDouble = !inDouble
			b.WriteByte(c)
		case c == '\'' && !inDouble:
			inSingle = !inSingle
			b.WriteByte('"')
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// closeBrackets counts unmatched { and [ (outside strings) and appends closers.
func closeBrackets(s string) string {
	var stack []byte
	inString := false

	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && inString && i+1 < len(s) {
			i++ // skip escaped char
			continue
		}
		if c == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch c {
		case '{':
			stack = append(stack, '}')
		case '[':
			stack = append(stack, ']')
		case '}', ']':
			if len(stack) > 0 && stack[len(stack)-1] == c {
				stack = stack[:len(stack)-1]
			}
		}
	}

	// Close in reverse order
	var b strings.Builder
	b.WriteString(s)
	for i := len(stack) - 1; i >= 0; i-- {
		b.WriteByte(stack[i])
	}
	return b.String()
}
