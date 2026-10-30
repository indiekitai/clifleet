// Package table formats the fleet matrix.
package table

import (
	"strings"
	"unicode/utf8"
)

// Render returns an aligned text table. The last column is painted when color is set.
func Render(headers []string, rows [][]string, color bool) string {
	if len(headers) == 0 {
		return ""
	}
	width := make([]int, len(headers))
	for i, h := range headers {
		width[i] = utf8.RuneCountInString(h)
	}
	for _, row := range rows {
		for i := 0; i < len(headers) && i < len(row); i++ {
			n := utf8.RuneCountInString(stripANSI(row[i]))
			if n > width[i] {
				width[i] = n
			}
		}
	}
	var b strings.Builder
	writeRow(&b, headers, width, false)
	b.WriteByte('\n')
	for i, row := range rows {
		cells := make([]string, len(headers))
		for c := 0; c < len(headers); c++ {
			if c < len(row) {
				cells[c] = row[c]
			}
		}
		if color {
			cells[len(cells)-1] = paint(cells[len(cells)-1])
		}
		writeRow(&b, cells, width, color)
		if i != len(rows)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func writeRow(b *strings.Builder, cells []string, width []int, colored bool) {
	for i, cell := range cells {
		if i > 0 {
			b.WriteString("  ")
		}
		b.WriteString(cell)
		if i == len(cells)-1 {
			continue
		}
		pad := width[i] - utf8.RuneCountInString(stripANSI(cell))
		if colored {
			pad = width[i] - utf8.RuneCountInString(stripANSI(cell))
		}
		if pad > 0 {
			b.WriteString(strings.Repeat(" ", pad))
		}
	}
}

func paint(status string) string {
	switch {
	case strings.HasPrefix(status, "installed"),
		strings.HasPrefix(status, "upgraded"),
		strings.HasPrefix(status, "up to date"):
		return "\033[32m" + status + "\033[0m"
	case strings.HasPrefix(status, "failed"),
		strings.HasPrefix(status, "unreachable"):
		return "\033[31m" + status + "\033[0m"
	case strings.HasPrefix(status, "update available"):
		return "\033[33m" + status + "\033[0m"
	default:
		return "\033[2m" + status + "\033[0m"
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
