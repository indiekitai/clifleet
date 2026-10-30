package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/indiekitai/clifleet/internal/fleet"
	"github.com/indiekitai/clifleet/internal/table"
)

func yes() *bool { b := true; return &b }

func behindReport() fleet.Report {
	return fleet.Report{Command: "status", Hosts: []fleet.HostResult{
		{Name: "mac-studio", Rows: []fleet.Row{
			{Tool: "claude", Version: "2.1.44", Latest: "2.1.44", Method: "native", Status: fleet.StCurrent, Installed: yes()},
			{Tool: "codex", Version: "0.163.0", Latest: "0.166.0", Method: "npm", Status: fleet.StBehind, Installed: yes()},
			{Tool: "kimi", Version: "1.0.0", Latest: "1.2.0", Method: "native", Status: fleet.StBehind, NoStage: true, Installed: yes()},
			{Tool: "agy", Status: fleet.StMissing},
		}},
		{Name: "omarchy", Rows: []fleet.Row{
			{Tool: "claude", Version: "2.1.40", Latest: "2.1.44", Method: "npm", Status: fleet.StBehind, Installed: yes()},
			{Tool: "pi", Version: "0.9.0", Latest: "1.0.0", Method: "native", Status: fleet.StBehind, NoStage: true, Installed: yes()},
			{Tool: "agy", Status: fleet.StMissing},
		}},
	}}
}

func TestStatusMatrixOutput(t *testing.T) {
	var out bytes.Buffer
	printStatus(&out, behindReport(), false, 0, false)
	got := out.String()
	t.Log("\n" + got)
	for _, want := range []string{"TOOL", "LATEST", "mac-studio", "omarchy", "↑ 0.163.0", "↑ 1.0.0*", "not installed anywhere: agy\n", "✓ up to date"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "HOST") || strings.Contains(got, "METHOD") {
		t.Fatalf("matrix printed the long table:\n%s", got)
	}
}

func TestStatusLongIsTheOldTable(t *testing.T) {
	r := behindReport()
	var out bytes.Buffer
	printStatus(&out, r, true, 0, false)
	want := table.Render(r.Header(), r.Lines(), false) + "\n" + r.Summary() + "\n"
	if out.String() != want {
		t.Fatalf("--long changed:\n%s\nwant\n%s", out.String(), want)
	}
}

func TestStatusFallsBackToLongWhenTooWide(t *testing.T) {
	r := behindReport()
	var narrow, long bytes.Buffer
	printStatus(&narrow, r, false, 20, false)
	printStatus(&long, r, true, 0, false)
	if narrow.String() != long.String() {
		t.Fatalf("narrow terminal did not get the long table:\n%s", narrow.String())
	}
	var wide bytes.Buffer
	printStatus(&wide, r, false, 200, false)
	if !strings.Contains(wide.String(), "not installed anywhere") {
		t.Fatal(wide.String())
	}
}

func TestTermWidth(t *testing.T) {
	var buf bytes.Buffer
	t.Setenv("COLUMNS", "")
	if termWidth(&buf) != 0 {
		t.Fatal("a pipe has no width limit")
	}
	t.Setenv("COLUMNS", "90")
	if termWidth(&buf) != 90 {
		t.Fatal("COLUMNS is ignored")
	}
}

func applyReport() fleet.Report {
	return fleet.Report{Command: "upgrade", Phase: fleet.PhaseApply, Hosts: []fleet.HostResult{
		{Name: "mbp", Rows: []fleet.Row{
			{Tool: "claude", Version: "2.1.40 → 2.1.44", Method: "npm", Status: fleet.StApplied},
			{Tool: "codex", Version: "0.166.0", Method: "npm", Status: fleet.StCurrent},
			{Tool: "agy", Status: fleet.StMissing},
			{Tool: "kimi", Version: "1.0.0", Status: fleet.StNoStage},
			{Tool: "pi", Version: "1.0.0", Method: "native", Status: fleet.StFailed, Detail: "timed out", Failed: true},
		}},
	}}
}

func TestUpgradeCompactTable(t *testing.T) {
	var out bytes.Buffer
	printUpgrade(&out, applyReport(), false, false, false)
	got := out.String()
	t.Log("\n" + got)
	for _, want := range []string{"claude", "applied", "pi", "failed (timed out)", "Not shown (3 rows", "1 not installed", "1 up to date", "1 no-stage", "1 applied"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "codex") || strings.Contains(got, "kimi") {
		t.Fatalf("untouched rows leaked into the table:\n%s", got)
	}
	var long bytes.Buffer
	printUpgrade(&long, applyReport(), true, false, false)
	if !strings.Contains(long.String(), "codex") || strings.Contains(long.String(), "Not shown") {
		t.Fatalf("--long must list every row:\n%s", long.String())
	}
}

func TestUpgradeDryRunSaysWhatWouldHappen(t *testing.T) {
	r := fleet.Report{Command: "upgrade", DryRun: true, Phase: fleet.PhaseFetch, Hosts: []fleet.HostResult{{Name: "h", Rows: []fleet.Row{
		{Tool: "codex", Version: "0.1.0", Latest: "0.166.0", Status: fleet.StDryRun, Detail: "npm pack @openai/codex@latest", Command: "npm pack @openai/codex@latest"},
	}}}}
	var out, long, verbose bytes.Buffer
	printUpgrade(&out, r, false, false, false)
	printUpgrade(&long, r, true, false, false)
	printUpgrade(&verbose, r, false, true, false)
	if !strings.Contains(out.String(), "would fetch → 0.166.0") || strings.Contains(out.String(), "npm pack") {
		t.Fatal(out.String())
	}
	if !strings.Contains(long.String(), "npm pack @openai/codex@latest") || !strings.Contains(verbose.String(), "npm pack @openai/codex@latest") {
		t.Fatal(long.String(), verbose.String())
	}
}

func TestNextHints(t *testing.T) {
	got := nextHints(behindReport(), "", false)
	if len(got) != 2 || !strings.HasPrefix(got[0], "Next: clifleet upgrade --fetch (download only), then clifleet upgrade --apply.") {
		t.Fatal(got)
	}
	if got[1] != "Only --one-shot can update: kimi (mac-studio), pi (omarchy)" {
		t.Fatal(got[1])
	}
	dry := behindReport()
	dry.DryRun = true
	if len(nextHints(dry, "", false)) != 0 {
		t.Fatal("no hint for a status dry run")
	}
	current := fleet.Report{Command: "status", Hosts: []fleet.HostResult{{Name: "a", Rows: []fleet.Row{{Tool: "x", Status: fleet.StCurrent}}}}}
	if len(nextHints(current, "", false)) != 0 {
		t.Fatal("nothing to say when everything is current")
	}

	staged := fleet.Report{Command: "upgrade", Phase: fleet.PhaseFetch, Hosts: []fleet.HostResult{{Name: "a", Rows: []fleet.Row{{Tool: "x", Status: fleet.StStaged}}}}}
	if got := nextHints(staged, fleet.PhaseFetch, false); len(got) != 1 || got[0] != "Next: clifleet upgrade --apply" {
		t.Fatal(got)
	}
	if got := nextHints(applyReport(), fleet.PhaseApply, false); len(got) != 1 ||
		got[0] != "Details: clifleet history --limit 20. Retry: clifleet upgrade --apply --hosts mbp --only pi" {
		t.Fatal(got)
	}
	if len(nextHints(upgradeNothing(), "", false)) != 0 {
		t.Fatal("nothing applies")
	}
}

func upgradeNothing() fleet.Report {
	return fleet.Report{Command: "upgrade", Hosts: []fleet.HostResult{{Name: "a", Rows: []fleet.Row{{Tool: "x", Status: fleet.StCurrent}}}}}
}

func TestNoHintsOutsideATerminal(t *testing.T) {
	// A bytes.Buffer is not a terminal, so stderr gets no hints. (Never run a
	// non-dry-run here: it would ssh to the example hosts.)
	path := writeExample(t)
	for _, args := range [][]string{
		{"status", "--dry-run", "-c", path},
	} {
		var stdout, stderr bytes.Buffer
		Execute(args, &stdout, &stderr)
		if strings.Contains(stderr.String(), "Next:") || strings.Contains(stderr.String(), "Details:") {
			t.Fatalf("%v printed a hint: %s", args, stderr.String())
		}
	}
}
