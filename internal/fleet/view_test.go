package fleet

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func yes() *bool { return boolPtr(true) }

func statusReport() Report {
	return Report{Command: "status", Hosts: []HostResult{
		{Name: "mbp", Rows: []Row{
			{Tool: "claude", Version: "2.1.44", Latest: "2.1.44", Status: StCurrent, Installed: yes()},
			{Tool: "codex", Version: "0.163.0", Latest: "0.166.0", Status: StBehind, Installed: yes()},
			{Tool: "kimi", Version: "1.0.0", Latest: "1.2.0", Status: StBehind, NoStage: true, Installed: yes()},
			{Tool: "agy", Status: StMissing},
			{Tool: "grok", Status: StMissing},
		}},
		{Name: "box", Rows: []Row{
			{Tool: "claude", Version: "2.1.40", Latest: "2.1.44", Status: StBehind, Installed: yes()},
			{Tool: "codex", Version: "unknown", VersionRaw: "keychain locked", Status: StInstalled, Installed: yes()},
			{Tool: "opencode", Version: "1.19.4", Status: StInstalled, Installed: yes()},
			{Tool: "agy", Status: StMissing},
		}},
	}}
}

func TestMatrixCellsLegendAndNotInstalled(t *testing.T) {
	m, ok := statusReport().Matrix(false)
	if !ok {
		t.Fatal("no matrix")
	}
	want := map[string][]string{
		"claude":   {"2.1.44", "✓", "↑ 2.1.40"},
		"codex":    {"0.166.0", "↑ 0.163.0", "?"},
		"kimi":     {"1.2.0", "↑ 1.0.0*", "·"},
		"opencode": {"—", "·", "1.19.4"},
	}
	if len(m.Rows) != len(want) {
		t.Fatalf("rows %v", m.Rows)
	}
	for _, row := range m.Rows {
		if got := strings.Join(row[1:], "|"); got != strings.Join(want[row[0]], "|") {
			t.Fatalf("%s: %q want %v", row[0], got, want[row[0]])
		}
	}
	if strings.Join(m.Header, " ") != "TOOL LATEST mbp box" {
		t.Fatal(m.Header)
	}
	if strings.Join(m.NotInstalled, ",") != "agy,grok" {
		t.Fatal(m.NotInstalled)
	}
	for _, part := range []string{"✓ up to date", "↑ <version> behind LATEST", "installed, newest unknown", "? version unreadable", "· not installed", "* only upgrade --one-shot"} {
		if !strings.Contains(m.Legend, part) {
			t.Fatalf("legend %q lacks %q", m.Legend, part)
		}
	}
	colored, _ := statusReport().Matrix(true)
	if !strings.Contains(colored.Rows[0][3], "\033[33m↑ 2.1.40") {
		t.Fatalf("behind cell is not yellow: %q", colored.Rows[0][3])
	}
}

func TestMatrixNotForDryRun(t *testing.T) {
	r := Report{Command: "status", DryRun: true, Hosts: []HostResult{{Name: "a", Rows: []Row{{Tool: "x", Status: StWouldProbe}}}}}
	if _, ok := r.Matrix(false); ok {
		t.Fatal("dry-run status must use the long table")
	}
}

func TestStatusJSONKeepsRowFields(t *testing.T) {
	r := statusReport()
	r.OneShot = true
	raw, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "NoStage") || strings.Contains(string(raw), "no_stage") || strings.Contains(string(raw), "OneShot") {
		t.Fatalf("new fields leaked into JSON: %s", raw)
	}
	var back struct {
		Hosts []struct {
			Tools []map[string]any `json:"tools"`
		} `json:"hosts"`
	}
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"name": true, "version": true, "latest": true, "version_raw": true, "method": true,
		"status": true, "detail": true, "path": true, "command": true, "installed": true}
	for _, h := range back.Hosts {
		for _, row := range h.Tools {
			for k := range row {
				if !allowed[k] {
					t.Fatalf("unexpected JSON key %q", k)
				}
			}
		}
	}
}

func upgradeReport() Report {
	return Report{Command: "upgrade", Hosts: []HostResult{
		{Name: "mbp", Rows: []Row{
			{Tool: "claude", Version: "2.1.40 → 2.1.44", Method: "native", Status: StApplied},
			{Tool: "codex", Version: "0.1.0", Status: StCurrent},
			{Tool: "agy", Status: StMissing}, {Tool: "grok", Status: StMissing},
			{Tool: "kimi", Version: "1.0.0", Status: StNoStage, Detail: "native updater"},
			{Tool: "cursor", Version: "1.0.0", Status: StSkipped, Detail: "no upgrade recipe"},
			{Tool: "pi", Version: "1.0.0", Status: StFailed, Detail: "timed out", Failed: true},
		}},
		{Name: "box", Rows: []Row{{Tool: "agy", Status: StMissing}, {Tool: "codex", Version: "0.1.0", Status: StCurrent}}},
	}}
}

func TestCompactHidesUntouchedRowsAndCounts(t *testing.T) {
	shown, hidden := upgradeReport().Compact()
	lines := shown.CompactLines()
	if len(lines) != 2 {
		t.Fatalf("lines %v", lines)
	}
	if lines[0][1] != "claude" || lines[1][1] != "pi" {
		t.Fatalf("lines %v", lines)
	}
	for _, h := range shown.Hosts {
		if h.Name == "box" {
			t.Fatal("a host with nothing to show must not print rows")
		}
	}
	line := hidden.Line()
	for _, part := range []string{"7 rows", "3 not installed", "2 up to date", "1 no-stage", "1 skipped"} {
		if !strings.Contains(line, part) {
			t.Fatalf("%q lacks %q", line, part)
		}
	}
	if len(upgradeReport().Lines()) != 9 {
		t.Fatal("Lines must still list every row")
	}
}

func TestDryRunWordsKeepCommandInLong(t *testing.T) {
	row := Row{Tool: "codex", Version: "0.1.0", Latest: "0.166.0", Status: StDryRun, Detail: "npm pack → npm i"}
	mk := func(phase string, one bool) Report {
		return Report{Command: "upgrade", DryRun: true, Phase: phase, OneShot: one, Hosts: []HostResult{{Name: "h", Rows: []Row{row}}}}
	}
	last := func(r Report) string { l := r.CompactLines()[0]; return l[len(l)-1] }
	if got := last(mk(PhaseFetch, false)); got != "would fetch → 0.166.0" {
		t.Fatal(got)
	}
	if got := last(mk(PhaseApply, false)); got != "would apply" {
		t.Fatal(got)
	}
	if got := last(mk("", false)); got != "would upgrade → 0.166.0" {
		t.Fatal(got)
	}
	if got := last(mk("", true)); got != "would run updater → 0.166.0" {
		t.Fatal(got)
	}
	long := mk(PhaseFetch, false).Lines()[0]
	if got := long[len(long)-1]; got != "dry-run: npm pack → npm i" {
		t.Fatalf("long lost the command: %q", got)
	}
}

func TestSummarySplitsNotInstalledFromSkipped(t *testing.T) {
	got := upgradeReport().Summary()
	if !strings.Contains(got, "1 skipped · 3 not installed") {
		t.Fatal(got)
	}
	r := Report{Command: "upgrade", Phase: PhaseFetch, Hosts: []HostResult{{Name: "h", Rows: []Row{
		{Tool: "a", Status: StMissing}, {Tool: "b", Status: StMissing}, {Tool: "c", Status: StSkipped}}}}}
	if s := r.Summary(); !strings.Contains(s, "1 skipped · 2 not installed") {
		t.Fatal(s)
	}
}

type lineSink struct {
	mu    sync.Mutex
	lines []string
}

func (s *lineSink) add(l string) {
	s.mu.Lock()
	s.lines = append(s.lines, l)
	s.mu.Unlock()
}

func (s *lineSink) joined() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return "\n" + strings.Join(s.lines, "\n") + "\n"
}

func TestProgressLinesPerToolAndHostCounter(t *testing.T) {
	cfg := parseFleet(t, `
[[hosts]]
name = "box"
ssh = "box"
tools = ["codex"]
`)
	pk := probeOut(withPkg(present("codex", "/usr/local/bin/codex", "/usr/local/lib/node_modules/@openai/codex/bin/codex", "0.1.0"), "@openai/codex"))
	fake := &fakeRunner{
		probe: map[string]string{"box": pk},
		fetch: map[string]string{"box": "clifleet-fetch/1\n" + actionLine("fetch", "codex", "0", "/cache/codex.tgz") + "\n"},
		apply: map[string]string{"box": strings.Join([]string{
			"clifleet-apply/1",
			actionLine("apply", "codex", "0", "/cache/codex.tgz"),
			"clifleet-probe/1",
			withPkg(present("codex", "/usr/local/bin/codex", "/usr/local/lib/node_modules/@openai/codex/bin/codex", "9.0.0"), "@openai/codex"),
		}, "\n") + "\n"},
	}
	sink := &lineSink{}
	_, err := Upgrade(context.Background(), cfg, Options{OnProgress: sink.add}, fake)
	if err != nil {
		t.Fatal(err)
	}
	got := sink.joined()
	for _, want := range []string{"\n→ box\n", "\n  box codex: fetching…\n", "\n  box codex: staged (", "\n  box codex: applying…\n", "\n  box codex: applied\n", "\n✓ box (1/1)\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("progress lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "(3m") || strings.Contains(got, "→ box (") {
		t.Fatalf("the per-host limit must not show as an estimate:\n%s", got)
	}

	failing := &fakeRunner{
		probe: map[string]string{"box": pk},
		fetch: map[string]string{"box": "clifleet-fetch/1\n" + actionLine("fetch", "codex", "1", "download failed") + "\n"},
	}
	sink = &lineSink{}
	if _, err := Upgrade(context.Background(), cfg, Options{Phase: PhaseFetch, OnProgress: sink.add}, failing); err != nil {
		t.Fatal(err)
	}
	if got := sink.joined(); !strings.Contains(got, "\n  box codex: failed (download failed)\n") || !strings.Contains(got, "\n✗ box (1/1)\n") {
		t.Fatalf("failed fetch progress:\n%s", got)
	}
}

func TestStatusMarksNoStageTools(t *testing.T) {
	cfg := parseFleet(t, `
[[hosts]]
name = "box"
ssh = "box"
tools = ["agy", "codex"]
`)
	fake := &fakeRunner{probe: map[string]string{"box": probeOut(
		present("agy", "/home/a/.local/bin/agy", "/home/a/.local/bin/agy", "1.0.0"),
		withPkg(present("codex", "/usr/local/bin/codex", "/usr/local/lib/node_modules/@openai/codex/bin/codex", "0.1.0"), "@openai/codex"),
	)}}
	got, err := Status(context.Background(), cfg, Options{}, fake)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range got.Hosts[0].Rows {
		if row.NoStage != (row.Tool == "agy") {
			t.Fatalf("%s NoStage=%v", row.Tool, row.NoStage)
		}
	}
}
