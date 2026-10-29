package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/indiekitai/clifleet/internal/fleet"
)

func sampleUpgrade(phase string) fleet.Report {
	return fleet.Report{
		Command: "upgrade",
		Phase:   phase,
		Hosts: []fleet.HostResult{
			{Name: "laptop", Rows: []fleet.Row{
				{Tool: "claude", Version: "2.1.40 → 2.1.44", Method: "native", Status: fleet.StApplied},
				{Tool: "codex", Status: fleet.StMissing},
				{Tool: "opencode", Version: "1.19.4", Method: "npm", Status: fleet.StFailed, Detail: "npm apply\nfailed\x1b[31m", Failed: true},
			}},
			{Name: "us-server", Error: "unreachable: ssh exit 255"},
		},
	}
}

func TestHistoryRecordsUpgradesOnly(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	at := time.Date(2026, 10, 29, 4, 31, 0, 0, time.UTC)

	dry := sampleUpgrade(fleet.PhaseApply)
	dry.DryRun = true
	status := sampleUpgrade("")
	status.Command = "status"
	for _, r := range []fleet.Report{dry, status} {
		if err := appendHistory(r, at); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(cache, "clifleet", "history.jsonl")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("dry run or status wrote history: %v", err)
	}

	if err := appendHistory(sampleUpgrade(fleet.PhaseFetch), at); err != nil {
		t.Fatal(err)
	}
	if err := appendHistory(sampleUpgrade(fleet.PhaseApply), at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	// Per run: laptop claude, laptop opencode, us-server error. codex was missing.
	if len(lines) != 6 {
		t.Fatalf("want 6 lines, got %d:\n%s", len(lines), raw)
	}
	var first historyEntry
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if first.Time != "2026-10-29T04:31:00Z" || first.Phase != "fetch" || first.Tool != "claude" || first.Version != "2.1.40 → 2.1.44" {
		t.Fatalf("%+v", first)
	}
	if strings.Contains(string(raw), "\x1b") {
		t.Fatal("control character written raw")
	}
}

func TestHistoryCommand(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	var out, errBuf bytes.Buffer
	if code := Execute([]string{"history"}, &out, &errBuf); code != 0 || !strings.Contains(out.String(), "no history yet") {
		t.Fatalf("empty history %d %q %q", code, out.String(), errBuf.String())
	}

	at := time.Date(2026, 10, 29, 4, 31, 0, 0, time.UTC)
	if err := appendHistory(sampleUpgrade(fleet.PhaseFetch), at); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cache, "clifleet", "history.jsonl")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("not json\n{\"time\":\"x\"}\n")
	f.Close()
	if err := appendHistory(sampleUpgrade(fleet.PhaseApply), at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// A crash mid-write leaves a cut-off last line.
	f, _ = os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	f.WriteString(`{"time":"2026-10-29T06:00:00Z","host":"lap`)
	f.Close()

	out.Reset()
	errBuf.Reset()
	if code := Execute([]string{"history"}, &out, &errBuf); code != 0 {
		t.Fatalf("exit %d %s", code, errBuf.String())
	}
	text := out.String()
	for _, want := range []string{"TIME", "PHASE", "2.1.40 → 2.1.44", "applied", "failed (npm apply failed ", "error (unreachable: ssh exit 255)"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in\n%s", want, text)
		}
	}
	if strings.Contains(text, "codex") || strings.Contains(text, "\x1b") {
		t.Fatalf("missing tool or raw escape in\n%s", text)
	}
	if n := strings.Count(text, "\n"); n != 7 {
		t.Fatalf("want header + 6 rows, got %d lines\n%s", n, text)
	}

	out.Reset()
	if code := Execute([]string{"history", "--hosts", "laptop", "--only", "opencode", "--limit", "1", "--json"}, &out, &errBuf); code != 0 {
		t.Fatal(code, errBuf.String())
	}
	var rows []historyEntry
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatal(err, out.String())
	}
	if len(rows) != 1 || rows[0].Tool != "opencode" || rows[0].Phase != "apply" || !rows[0].Failed {
		t.Fatalf("%+v", rows)
	}

	out.Reset()
	if code := Execute([]string{"history", "--only", "nope", "--json"}, &out, &errBuf); code != 0 || strings.TrimSpace(out.String()) != "[]" {
		t.Fatalf("%d %q", code, out.String())
	}
	for _, bad := range [][]string{{"history", "extra"}, {"history", "--limit", "-1"}, {"history", "--timeout", "5s"}} {
		if code := Execute(bad, &out, &errBuf); code != 1 {
			t.Fatalf("%v exit %d", bad, code)
		}
	}
}
