package table

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRenderAligns(t *testing.T) {
	got := Render(
		[]string{"HOST", "TOOL", "VERSION", "METHOD", "STATUS"},
		[][]string{
			{"us-server", "claude", "2.1.44", "native", "installed"},
			{"us-server", "codex", "—", "—", "not installed"},
		},
		false,
	)
	if !strings.Contains(got, "not installed") || !strings.Contains(got, "—") {
		t.Fatal(got)
	}
	lines := strings.Split(got, "\n")
	if len(lines) != 3 {
		t.Fatalf("lines %d\n%s", len(lines), got)
	}
}

// Cells that arrive already colored (the status matrix) pad by their visible
// width, not by their bytes.
func TestRenderIgnoresColorCodesInWidth(t *testing.T) {
	got := Render([]string{"CELL", "TOOL"}, [][]string{
		{"\033[33m↑ 0.2.1-rc.2\033[0m", "dsh"},
		{"✓", "claude"},
	}, false)
	lines := strings.Split(got, "\n")
	if last := lines[len(lines)-1]; utf8.RuneCountInString(last) != len("↑ 0.2.1-rc.2  claude")-2 {
		t.Fatalf("%q\n%s", last, got)
	}
}
