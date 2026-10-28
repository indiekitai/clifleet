package table

import (
	"strings"
	"testing"
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
