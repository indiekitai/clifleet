// Package shellquote quotes argv for a POSIX shell.
package shellquote

import (
	"regexp"
	"strings"
)

// Quote returns s as a single POSIX single-quoted word.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Format quotes every argument and joins them with spaces.
func Format(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = Quote(a)
	}
	return strings.Join(parts, " ")
}

var bare = regexp.MustCompile(`^[A-Za-z0-9_@+./:=,-]+$`)

// Display quotes only arguments that are not safe bare words.
func Display(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		if a != "" && bare.MatchString(a) {
			parts[i] = a
		} else {
			parts[i] = Quote(a)
		}
	}
	return strings.Join(parts, " ")
}
