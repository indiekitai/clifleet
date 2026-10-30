package fleet

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/indiekitai/clifleet/internal/catalog"
	"github.com/indiekitai/clifleet/internal/config"
	"github.com/indiekitai/clifleet/internal/runner"
)

func TestStatusDryRunRespectsPolicy(t *testing.T) {
	cfg := testFleet(t)
	report, err := Status(context.Background(), cfg, Options{DryRun: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Failed() {
		t.Fatal("dry-run should not fail")
	}
	if !strings.Contains(report.Summary(), "no connections opened") {
		t.Fatal(report.Summary())
	}
	seen := map[string]bool{}
	for _, line := range report.Lines() {
		key := line[0] + "/" + line[1]
		seen[key] = true
		if line[4] != StWouldProbe {
			t.Fatalf("status %v", line)
		}
	}
	if seen["us-server/opencode"] || seen["us-server/dsh"] || seen["workshop/opencode"] {
		t.Fatalf("policy leak %#v", seen)
	}
	if !seen["us-server/claude"] || !seen["shanghai/dsh"] || !seen["workshop/claude"] {
		t.Fatalf("missing rows %#v", seen)
	}
}

func TestUpgradeSkipsMissingAndFailsClosed(t *testing.T) {
	cfg := testFleet(t)
	// Only the two SSH hosts, so the test does not depend on the laptop row.
	fake := &fakeRunner{
		probe: map[string]string{
			"us-server": probeOut(
				present("claude", "/home/a/.local/bin/claude", "/home/a/.local/bin/claude", "2.1.40"),
				missing("codex"),
			),
			"shanghai": "",
		},
		upgrade: map[string]string{
			"us-server": upgradeOut("claude", 0, present("claude", "/home/a/.local/bin/claude", "/home/a/.local/bin/claude", "2.1.44")),
		},
		fail: map[string]error{
			"shanghai": errString("connection refused"),
		},
	}
	report, err := Upgrade(context.Background(), cfg, Options{
		Hosts:    []string{"us-server", "shanghai"},
		Parallel: 2,
		Timeout:  time.Second,
		OneShot:  true,
	}, fake)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Failed() {
		t.Fatal("shanghai should fail the run")
	}
	var claude, codex string
	for _, h := range report.Hosts {
		if h.Name != "us-server" {
			continue
		}
		for _, row := range h.Rows {
			switch row.Tool {
			case "claude":
				claude = row.Status + " " + row.Version + " " + row.Command
			case "codex":
				codex = row.Status
				if row.Command != "" {
					t.Fatalf("missing codex got a command %q", row.Command)
				}
			case "opencode", "dsh":
				t.Fatalf("us-server row for %s", row.Tool)
			}
		}
	}
	if !strings.Contains(claude, StUpgraded) || !strings.Contains(claude, "2.1.40 → 2.1.44") || !strings.Contains(claude, "claude update") {
		t.Fatalf("claude row %q", claude)
	}
	if codex != StMissing {
		t.Fatalf("codex %q", codex)
	}
	scripts := fake.Scripts()
	joined := strings.Join(scripts, "\n---\n")
	if strings.Contains(joined, "'codex' 'update'") || strings.Contains(joined, "@openai/codex") || strings.Contains(joined, "brew install") {
		t.Fatalf("missing tool was upgraded:\n%s", joined)
	}
	if !strings.Contains(joined, "'claude' 'update' </dev/null") {
		t.Fatalf("claude was not upgraded:\n%s", joined)
	}
	if strings.Count(joined, "clifleet-upgrade/1") != 1 {
		t.Fatalf("expected one upgrade script, got\n%s", joined)
	}
}

func TestUpgradeDryRunDoesNotApply(t *testing.T) {
	cfg := testFleet(t)
	fake := &fakeRunner{
		probe: map[string]string{
			"us-server": probeOut(
				withBrew(present("claude", "/opt/homebrew/bin/claude", "/opt/homebrew/Caskroom/claude-code/2.1.44/claude", "2.1.44"), "cask", "claude-code"),
				missing("codex"),
			),
		},
	}
	report, err := Upgrade(context.Background(), cfg, Options{Hosts: []string{"us-server"}, DryRun: true}, fake)
	if err != nil {
		t.Fatal(err)
	}
	if report.Failed() {
		t.Fatalf("errors %v", report.Errors())
	}
	found := false
	for _, line := range report.Lines() {
		if line[0] == "us-server" && line[1] == "claude" {
			found = true
			if line[3] != "brew" || !strings.Contains(line[4], "brew fetch --cask claude-code") || !strings.Contains(line[4], "brew upgrade --cask claude-code") {
				t.Fatalf("line %v", line)
			}
		}
		if line[1] == "codex" && line[4] != StMissing {
			t.Fatalf("codex %v", line)
		}
	}
	if !found {
		t.Fatal(report.Lines())
	}
	for _, s := range fake.Scripts() {
		if strings.Contains(s, "clifleet-upgrade/1") {
			t.Fatal("dry-run executed an upgrade")
		}
	}
}

func TestFetchThenApply(t *testing.T) {
	cfg := parseFleet(t, `
[[hosts]]
name = "box"
ssh = "box"
tools = ["claude", "codex", "cursor", "opencode"]
`)
	probe := probeOut(
		present("claude", "/home/a/.local/bin/claude", "/home/a/.local/share/claude/versions/2.1.40", "2.1.40"),
		withPkg(present("codex", "/usr/local/bin/codex", "/usr/local/lib/node_modules/@openai/codex/bin/codex", "0.163.0"), "@openai/codex"),
		present("cursor", "/usr/local/bin/cursor", "/Applications/Cursor.app/Contents/Resources/app/bin/cursor", "3.0.0"),
		withBrew(present("opencode", "/opt/homebrew/bin/opencode", "/opt/homebrew/Cellar/opencode/1.2.3/bin/opencode", "1.2.3"), "formula", "opencode"),
	)
	fake := &fakeRunner{probe: map[string]string{"box": probe}}

	dry, err := Upgrade(context.Background(), cfg, Options{DryRun: true, Phase: PhaseFetch}, fake)
	if err != nil {
		t.Fatal(err)
	}
	if dry.Failed() || dry.Phase != PhaseFetch {
		t.Fatalf("dry %+v %s", dry.Failed(), dry.Summary())
	}
	if !strings.Contains(dry.Summary(), "would stage") || !strings.Contains(dry.Summary(), "no-stage") {
		t.Fatal(dry.Summary())
	}
	saw := map[string]string{}
	for _, line := range dry.Lines() {
		saw[line[1]] = strings.Join(line, " | ")
	}
	if !strings.Contains(saw["claude"], "dry-run:") || !strings.Contains(saw["claude"], "downloads.claude.ai/claude-code-releases/") {
		t.Fatalf("claude %s", saw["claude"])
	}
	if !strings.Contains(saw["codex"], "dry-run:") || !strings.Contains(saw["codex"], "npm pack") || !strings.Contains(saw["codex"], "@openai/codex@latest") {
		t.Fatalf("codex %s", saw["codex"])
	}
	if strings.Contains(saw["codex"], "npm install") {
		t.Fatalf("fetch dry-run installed: %s", saw["codex"])
	}
	if !strings.Contains(saw["cursor"], "skipped") || !strings.Contains(saw["cursor"], "no upgrade recipe") {
		t.Fatalf("cursor %s", saw["cursor"])
	}
	if !strings.Contains(saw["opencode"], "brew update && brew fetch --formula opencode") {
		t.Fatalf("opencode %s", saw["opencode"])
	}
	for _, s := range fake.Scripts() {
		if strings.Contains(s, "clifleet-fetch/1") || strings.Contains(s, "clifleet-upgrade/1") || strings.Contains(s, "clifleet-apply/1") {
			t.Fatal("dry-run ran a stage or upgrade")
		}
	}

	fake = &fakeRunner{
		probe: map[string]string{"box": probe},
		fetch: map[string]string{"box": strings.Join([]string{
			"clifleet-fetch/1",
			actionLine("fetch", "claude", "0", "/home/a/.cache/clifleet/release/claude/claude.tar.gz"),
			actionLine("fetch", "codex", "0", "/home/a/.cache/clifleet/npm/codex/openai-codex-9.9.9.tgz"),
			actionLine("fetch", "opencode", "0", "brew cache (formula opencode)"),
		}, "\n") + "\n"},
	}
	got, err := Upgrade(context.Background(), cfg, Options{Phase: PhaseFetch}, fake)
	if err != nil || got.Failed() {
		t.Fatalf("%v %s %v", err, got.Summary(), got.Errors())
	}
	if !strings.Contains(got.Summary(), "3 staged") || !strings.Contains(got.Summary(), "0 no-stage") {
		t.Fatal(got.Summary())
	}
	joined := strings.Join(fake.Scripts(), "\n---\n")
	if !strings.Contains(joined, "stage_npm 'codex' '@openai/codex'") || !strings.Contains(joined, "stage_brew 'opencode' 'formula' 'opencode'") {
		t.Fatalf("fetch script:\n%s", joined)
	}
	if strings.Contains(joined, "'claude' 'update'") || strings.Contains(joined, "npm install") || strings.Contains(joined, "stage_npm 'cursor'") {
		t.Fatalf("fetch touched a live or unstaged tool:\n%s", joined)
	}
	if !strings.Contains(joined, "stage_release_claude 'claude'") {
		t.Fatalf("native claude was not staged:\n%s", joined)
	}
	for _, h := range got.Hosts {
		for _, row := range h.Rows {
			switch row.Tool {
			case "codex":
				if row.Status != StStaged || !strings.Contains(row.Detail, "openai-codex-9.9.9.tgz") {
					t.Fatalf("codex %+v", row)
				}
			case "claude":
				if row.Status != StStaged {
					t.Fatalf("claude %+v", row)
				}
			}
		}
	}

	fake = &fakeRunner{
		probe: map[string]string{"box": probe},
		apply: map[string]string{"box": strings.Join([]string{
			"clifleet-apply/1",
			actionLine("apply", "claude", "0", "staged claude"),
			actionLine("apply", "codex", "skip", "nothing staged"),
			actionLine("apply", "opencode", "0", "brew cache (formula opencode)"),
			"clifleet-probe/1",
			withBrew(present("opencode", "/opt/homebrew/bin/opencode", "/opt/homebrew/Cellar/opencode/1.3.0/bin/opencode", "1.3.0"), "formula", "opencode"),
			withPkg(present("codex", "/usr/local/bin/codex", "/usr/local/lib/node_modules/@openai/codex/bin/codex", "0.163.0"), "@openai/codex"),
			present("claude", "/home/a/.local/bin/claude", "/home/a/.local/bin/claude", "2.1.44"),
			present("cursor", "/usr/local/bin/cursor", "/Applications/Cursor.app/Contents/Resources/app/bin/cursor", "3.0.0"),
		}, "\n") + "\n"},
	}
	applied, err := Upgrade(context.Background(), cfg, Options{Phase: PhaseApply}, fake)
	if err != nil || applied.Failed() {
		t.Fatalf("apply should skip a missing stamp without failing the host: %v %s", err, applied.Summary())
	}
	if !strings.Contains(applied.Summary(), "2 applied") {
		t.Fatal(applied.Summary())
	}
	applyScript := strings.Join(fake.Scripts(), "\n")
	if strings.Contains(applyScript, "npm pack") || strings.Contains(applyScript, "@latest") || strings.Contains(applyScript, "brew fetch") {
		t.Fatalf("apply re-downloaded:\n%s", applyScript)
	}
	if !strings.Contains(applyScript, "apply_npm 'codex'") || !strings.Contains(applyScript, "apply_brew 'opencode'") || !strings.Contains(applyScript, "apply_release_claude 'claude'") {
		t.Fatalf("apply script:\n%s", applyScript)
	}
	if strings.Contains(applyScript, "apply_npm 'claude'") || strings.Contains(applyScript, "apply_brew 'cursor'") {
		t.Fatalf("apply ran for a tool that cannot be staged:\n%s", applyScript)
	}
	for _, h := range applied.Hosts {
		for _, row := range h.Rows {
			switch row.Tool {
			case "codex":
				if row.Status != StSkipped || row.Detail != "nothing staged" {
					t.Fatalf("codex %+v", row)
				}
			case "opencode":
				if row.Status != StApplied || !strings.Contains(row.Version, "1.2.3 → 1.3.0") {
					t.Fatalf("opencode %+v", row)
				}
			case "claude":
				if row.Status != StApplied || !strings.Contains(row.Version, "2.1.40 → 2.1.44") {
					t.Fatalf("claude %+v", row)
				}
			}
		}
	}

	// The one-shot path still upgrades native binaries in place.
	fake = &fakeRunner{
		probe: map[string]string{"box": probe},
		upgrade: map[string]string{"box": upgradeOut("claude", 0,
			present("claude", "/home/a/.local/bin/claude", "/home/a/.local/bin/claude", "2.1.44"),
		)},
	}
	oneShot, err := Upgrade(context.Background(), cfg, Options{Only: []string{"claude"}, OneShot: true}, fake)
	if err != nil || oneShot.Failed() {
		t.Fatalf("one-shot %v %s", err, oneShot.Summary())
	}
	shot := strings.Join(fake.Scripts(), "\n")
	if !strings.Contains(shot, "'claude' 'update' </dev/null") || strings.Contains(shot, "clifleet-fetch/1") || strings.Contains(shot, "npm pack") {
		t.Fatalf("one-shot changed:\n%s", shot)
	}
}

func TestApplyDryRunShowsLocalInstall(t *testing.T) {
	cfg := parseFleet(t, `
[[hosts]]
name = "box"
ssh = "box"
tools = ["codex"]
`)
	fake := &fakeRunner{probe: map[string]string{"box": probeOut(
		withPkg(present("codex", "/usr/local/bin/codex", "/usr/local/lib/node_modules/@openai/codex/bin/codex", "0.163.0"), "@openai/codex"),
	)}}
	report, err := Upgrade(context.Background(), cfg, Options{DryRun: true, Phase: PhaseApply}, fake)
	if err != nil || report.Failed() {
		t.Fatal(err, report.Summary())
	}
	line := strings.Join(report.Lines()[0], " | ")
	if !strings.Contains(line, "npm install -g") || !strings.Contains(line, "--prefix") || !strings.Contains(line, "<staged>.tgz") || strings.Contains(line, "@latest") {
		t.Fatalf("apply dry-run %s", line)
	}
	if !strings.Contains(report.Summary(), "would apply") {
		t.Fatal(report.Summary())
	}
}

func parseFleet(t *testing.T, body string) config.Loaded {
	t.Helper()
	cfg, err := config.Parse("hosts.toml", []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func testFleet(t *testing.T) config.Loaded {
	t.Helper()
	cfg, err := config.Parse("hosts.toml", []byte(config.Example))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func probeOut(lines ...string) string {
	return "clifleet-probe/1\n" + strings.Join(lines, "\n") + "\n"
}

func upgradeOut(tool string, code int, reprobe ...string) string {
	head := "clifleet-upgrade/1\n" + strings.Join([]string{"CF1", "upgrade", tool, itoa(code)}, "\t") + "\n"
	return head + probeOut(reprobe...)
}

func present(tool, path, real, version string) string {
	return strings.Join([]string{"CF1", "present", tool, path, real, version, "", "", ""}, "\t")
}

func withPkg(line, pkg string) string {
	parts := strings.Split(line, "\t")
	parts[8] = pkg
	return strings.Join(parts, "\t")
}

func actionLine(tag, tool, code, detail string) string {
	return strings.Join([]string{"CF1", tag, tool, code, detail}, "\t")
}

func withBrew(line, kind, token string) string {
	parts := strings.Split(line, "\t")
	parts[6] = kind
	parts[7] = token
	return strings.Join(parts, "\t")
}

func missing(tool string) string {
	return strings.Join([]string{"CF1", "missing", tool}, "\t")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	return "1"
}

type errString string

func (e errString) Error() string { return string(e) }

type fakeRunner struct {
	mu       sync.Mutex
	scripts  []string
	timeouts []time.Duration
	probe    map[string]string
	upgrade  map[string]string
	fetch    map[string]string
	apply    map[string]string
	fail     map[string]error
}

func (f *fakeRunner) Run(_ context.Context, target runner.Target, script string) (string, string, error) {
	f.mu.Lock()
	f.scripts = append(f.scripts, target.Name+"\n"+script)
	f.timeouts = append(f.timeouts, target.Timeout)
	f.mu.Unlock()
	if err := f.fail[target.Name]; err != nil {
		return "", err.Error(), err
	}
	switch {
	case strings.Contains(script, "clifleet-upgrade/1"):
		return f.upgrade[target.Name], "", nil
	case strings.Contains(script, "clifleet-fetch/1"):
		return f.fetch[target.Name], "", nil
	case strings.Contains(script, "clifleet-apply/1"):
		return f.apply[target.Name], "", nil
	}
	return f.probe[target.Name], "", nil
}

func (f *fakeRunner) Scripts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.scripts))
	copy(out, f.scripts)
	return out
}

func (f *fakeRunner) Timeouts() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]time.Duration, len(f.timeouts))
	copy(out, f.timeouts)
	return out
}

func TestFetchFailureDoesNotPoisonSiblings(t *testing.T) {
	cfg := parseFleet(t, `
[[hosts]]
name = "box"
ssh = "box"
tools = ["codex", "opencode"]
`)
	probe := probeOut(
		withPkg(present("codex", "/usr/local/bin/codex", "/usr/local/lib/node_modules/@openai/codex/bin/codex", "0.1.0"), "@openai/codex"),
		withBrew(present("opencode", "/opt/homebrew/bin/opencode", "/opt/homebrew/Cellar/opencode/1.0.0/bin/opencode", "1.0.0"), "formula", "opencode"),
	)
	fake := &selectiveRunner{probe: probe}
	got, err := Upgrade(context.Background(), cfg, Options{Phase: PhaseFetch, Timeout: 10 * time.Minute}, fake)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Failed() {
		t.Fatal("the slow tool should still fail the run")
	}
	saw := map[string]Row{}
	for _, h := range got.Hosts {
		if h.Error != "" {
			t.Fatalf("host error poisoned the fleet: %s", h.Error)
		}
		for _, row := range h.Rows {
			saw[row.Tool] = row
		}
	}
	if saw["codex"].Status != StStaged || saw["codex"].Failed {
		t.Fatalf("codex should have staged despite brew: %+v", saw["codex"])
	}
	if saw["opencode"].Status != StFailed || !strings.Contains(saw["opencode"].Detail, "timed out") {
		t.Fatalf("opencode %+v", saw["opencode"])
	}
	var fetches int
	for _, script := range fake.scripts {
		if strings.Contains(script, "clifleet-fetch/1") {
			fetches++
		}
	}
	if fetches != 2 {
		t.Fatalf("expected one fetch process per tool, got %d", fetches)
	}
}

func TestFetchTimeoutKeepsPartialSuccess(t *testing.T) {
	cfg := parseFleet(t, `
[[hosts]]
name = "box"
ssh = "box"
tools = ["codex", "opencode"]
`)
	probe := probeOut(
		withPkg(present("codex", "/usr/local/bin/codex", "/usr/local/lib/node_modules/@openai/codex/bin/codex", "0.1.0"), "@openai/codex"),
		withBrew(present("opencode", "/opt/homebrew/bin/opencode", "/opt/homebrew/Cellar/opencode/1.0.0/bin/opencode", "1.0.0"), "formula", "opencode"),
	)
	fake := &selectiveRunner{
		probe:   probe,
		partial: "clifleet-fetch/1\n" + actionLine("fetch", "codex", "0", "/cache/codex.tgz") + "\n",
	}
	got, err := Upgrade(context.Background(), cfg, Options{Phase: PhaseFetch}, fake)
	if err != nil {
		t.Fatal(err)
	}
	saw := map[string]Row{}
	for _, h := range got.Hosts {
		for _, row := range h.Rows {
			saw[row.Tool] = row
		}
	}
	if saw["codex"].Status != StStaged || saw["codex"].Failed {
		t.Fatalf("partial fetch should stay staged: %+v", saw["codex"])
	}
	if saw["opencode"].Status != StFailed {
		t.Fatalf("opencode %+v", saw["opencode"])
	}
}

func TestDefaultUpgradeFetchesBeforeApply(t *testing.T) {
	cfg := parseFleet(t, `
[[hosts]]
name = "box"
ssh = "box"
tools = ["agy", "codex"]
`)
	probe := probeOut(
		present("agy", "/home/a/.local/bin/agy", "/home/a/.local/bin/agy", "1.0.0"),
		withPkg(present("codex", "/usr/local/bin/codex", "/usr/local/lib/node_modules/@openai/codex/bin/codex", "0.1.0"), "@openai/codex"),
	)
	fake := &fakeRunner{
		probe: map[string]string{"box": probe},
		fetch: map[string]string{"box": "clifleet-fetch/1\n" + actionLine("fetch", "codex", "0", "/cache/codex.tgz") + "\n"},
		apply: map[string]string{"box": strings.Join([]string{
			"clifleet-apply/1",
			actionLine("apply", "codex", "0", "/cache/codex.tgz"),
			"clifleet-probe/1",
			withPkg(present("codex", "/usr/local/bin/codex", "/usr/local/lib/node_modules/@openai/codex/bin/codex", "9.0.0"), "@openai/codex"),
			present("agy", "/home/a/.local/bin/agy", "/home/a/.local/bin/agy", "1.0.0"),
		}, "\n") + "\n"},
	}
	got, err := Upgrade(context.Background(), cfg, Options{}, fake)
	if err != nil || got.Failed() {
		t.Fatalf("%v %s %v", err, got.Summary(), got.Errors())
	}
	if !strings.Contains(got.Summary(), "1 upgraded") || !strings.Contains(got.Summary(), "1 no-stage") {
		t.Fatal(got.Summary())
	}
	joined := strings.Join(fake.Scripts(), "\n")
	if strings.Contains(joined, "clifleet-upgrade/1") || strings.Contains(joined, "'agy' 'update'") {
		t.Fatalf("default upgrade one-shot a native tool:\n%s", joined)
	}
	if !strings.Contains(joined, "stage_npm 'codex'") || !strings.Contains(joined, "apply_npm 'codex'") {
		t.Fatalf("default upgrade did not fetch then apply:\n%s", joined)
	}
	for _, h := range got.Hosts {
		for _, row := range h.Rows {
			switch row.Tool {
			case "codex":
				if row.Status != StApplied || !strings.Contains(row.Version, "0.1.0 → 9.0.0") {
					t.Fatalf("codex %+v", row)
				}
			case "agy":
				if row.Status != StNoStage {
					t.Fatalf("agy %+v", row)
				}
			}
		}
	}
}

func TestNoteVersionKeepsInstallWhenProbeIsUnreadable(t *testing.T) {
	row := Row{Tool: "opencode", Version: "1.0.0", Status: StApplied, Detail: "brew cache (formula opencode)"}
	got := noteVersion(row, "1.0.0", catalog.Observation{
		Present: true, Version: "version timed out", Path: "/opt/homebrew/bin/opencode",
	}, StApplied)
	if strings.Contains(got.Version, "version timed out") || got.Status != StApplied {
		t.Fatalf("%+v", got)
	}
	if !strings.Contains(got.Detail, "version timed out") || got.Version != "1.0.0" {
		t.Fatalf("%+v", got)
	}
}

func TestSummaryCountsFailedToolRows(t *testing.T) {
	oneHost := Report{
		Command: "upgrade",
		Phase:   PhaseFetch,
		Hosts: []HostResult{{
			Name: "box",
			Rows: []Row{
				{Tool: "codex", Status: StFailed, Failed: true},
				{Tool: "claude", Status: StFailed, Failed: true},
				{Tool: "opencode", Status: StFailed, Failed: true},
				{Tool: "agy", Status: StStaged},
			},
		}},
	}
	if !strings.Contains(oneHost.Summary(), "3 failed (1 host)") || !strings.Contains(oneHost.Summary(), "1 staged") {
		t.Fatal(oneHost.Summary())
	}
	two := Report{
		Command: "upgrade",
		Phase:   PhaseFetch,
		Hosts: []HostResult{
			{Name: "a", Rows: []Row{{Tool: "codex", Status: StFailed, Failed: true}}},
			{Name: "b", Rows: []Row{{Tool: "claude", Status: StStaged}}},
		},
	}
	if !strings.Contains(two.Summary(), "1 failed") || strings.Contains(two.Summary(), "(1 host)") {
		t.Fatal(two.Summary())
	}
	if !strings.Contains(two.Summary(), "1 staged") {
		t.Fatal(two.Summary())
	}
}

func TestMiseShimIsNotStaged(t *testing.T) {
	cfg := parseFleet(t, `
[[hosts]]
name = "omarchy"
ssh = "omarchy"
tools = ["codex"]
`)
	fake := &fakeRunner{probe: map[string]string{
		"omarchy": probeOut(present("codex", "/home/a/.local/share/mise/shims/codex", "/usr/bin/mise", "0.99.0")),
	}}
	got, err := Upgrade(context.Background(), cfg, Options{Phase: PhaseFetch}, fake)
	if err != nil {
		t.Fatal(err)
	}
	if got.Failed() {
		t.Fatalf("managed by mise should not fail the run: %s", got.Summary())
	}
	row := got.Hosts[0].Rows[0]
	if row.Status != StSkipped || !strings.Contains(row.Detail, "managed by mise") || row.Method != "mise" {
		t.Fatalf("%+v", row)
	}
	for _, script := range fake.Scripts() {
		if strings.Contains(script, "stage_release_codex") || strings.Contains(script, "clifleet-fetch/1") {
			t.Fatalf("mise shim was staged:\n%s", script)
		}
	}
}

func TestBlockedEngineIsNotAFailedApply(t *testing.T) {
	cfg := parseFleet(t, `
[[hosts]]
name = "box"
ssh = "box"
tools = ["openclaw"]
`)
	probe := probeOut(withPkg(
		present("openclaw", "/usr/local/bin/openclaw", "/usr/local/lib/node_modules/openclaw/bin.js", "2026.10.1"),
		"openclaw",
	))
	fake := &fakeRunner{
		probe: map[string]string{"box": probe},
		fetch: map[string]string{"box": "clifleet-fetch/1\n" + actionLine("fetch", "openclaw", "blocked", "requires Node >=24.22.1 <25 || >=26.12.1 (host has v22.11.0)") + "\n"},
	}
	got, err := Upgrade(context.Background(), cfg, Options{Phase: PhaseFetch}, fake)
	if err != nil {
		t.Fatal(err)
	}
	if got.Failed() {
		t.Fatalf("blocked should not fail the run: %s %v", got.Summary(), got.Errors())
	}
	if !strings.Contains(got.Summary(), "1 blocked") {
		t.Fatal(got.Summary())
	}
	row := got.Hosts[0].Rows[0]
	if row.Status != StBlocked || !strings.Contains(row.Detail, "24.22.1") || !strings.Contains(row.Detail, "v22.11.0") {
		t.Fatalf("%+v", row)
	}
}

type selectiveRunner struct {
	probe   string
	partial string
	scripts []string
}

func (s *selectiveRunner) Run(_ context.Context, target runner.Target, script string) (string, string, error) {
	s.scripts = append(s.scripts, target.Name+"\n"+script)
	switch {
	case strings.Contains(script, "stage_brew 'opencode'"):
		return "", "brew fetch hung", errString("timed out after 10m0s")
	case strings.Contains(script, "stage_npm 'codex'") && s.partial != "":
		return s.partial, "timed out after the codex line", errString("timed out after 10m0s")
	case strings.Contains(script, "clifleet-fetch/1"):
		return "clifleet-fetch/1\n" + actionLine("fetch", "codex", "0", "/cache/codex.tgz") + "\n", "", nil
	default:
		return s.probe, "", nil
	}
}

func TestStatusBudgetAndUnreachableDetail(t *testing.T) {
	cfg := parseFleet(t, "[[hosts]]\nname = \"box\"\nssh = \"box\"\ntools = [\"claude\"]\n")
	if cfg.TimeoutExplicit || cfg.Timeout != 3*time.Minute {
		t.Fatalf("implicit timeout explicit=%v %s", cfg.TimeoutExplicit, cfg.Timeout)
	}
	fake := &fakeRunner{fail: map[string]error{"box": errString("connection refused")}}
	report, err := Status(context.Background(), cfg, Options{}, fake)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Failed() {
		t.Fatal("expected unreachable")
	}
	got := fake.Timeouts()
	if len(got) != 1 || got[0] != StatusBudget {
		t.Fatalf("status budget %v", got)
	}
	line := strings.Join(report.Lines()[0], " | ")
	if !strings.Contains(line, "unreachable (connection refused)") {
		t.Fatal(line)
	}
	if len(report.Errors()) != 1 || !strings.Contains(report.Errors()[0], "box: connection refused") {
		t.Fatalf("errors %v", report.Errors())
	}

	fake = &fakeRunner{probe: map[string]string{"box": probeOut(missing("claude"))}}
	if _, err := Upgrade(context.Background(), cfg, Options{}, fake); err != nil {
		t.Fatal(err)
	}
	got = fake.Timeouts()
	if len(got) != 1 || got[0] != 3*time.Minute {
		t.Fatalf("upgrade budget %v", got)
	}

	fake = &fakeRunner{probe: map[string]string{"box": probeOut(missing("claude"))}}
	if _, err := Status(context.Background(), cfg, Options{Timeout: 10 * time.Second}, fake); err != nil {
		t.Fatal(err)
	}
	got = fake.Timeouts()
	if len(got) != 1 || got[0] != 10*time.Second {
		t.Fatalf("cli timeout %v", got)
	}

	explicit := testFleet(t)
	if !explicit.TimeoutExplicit {
		t.Fatal("example timeout should be explicit")
	}
}

type scriptRunner func(script string) (string, string, error)

func (f scriptRunner) Run(_ context.Context, _ runner.Target, script string) (string, string, error) {
	return f(script)
}

func TestApplyTimeoutKeepsPartialSuccess(t *testing.T) {
	cfg := parseFleet(t, `
[[hosts]]
name = "box"
ssh = "box"
tools = ["codex", "opencode"]
`)
	probe := probeOut(
		withPkg(present("codex", "/usr/local/bin/codex", "/usr/local/lib/node_modules/@openai/codex/bin/codex", "0.1.0"), "@openai/codex"),
		withBrew(present("opencode", "/opt/homebrew/bin/opencode", "/opt/homebrew/Cellar/opencode/1.0.0/bin/opencode", "1.0.0"), "formula", "opencode"),
	)
	fake := scriptRunner(func(script string) (string, string, error) {
		switch {
		case strings.Contains(script, "apply_npm 'codex'"):
			// The swap finished and was reported; the re-probe after it timed out.
			return "clifleet-apply/1\n" + actionLine("apply", "codex", "0", "swapped") + "\n", "", errString("timed out after 10m0s")
		case strings.Contains(script, "apply_brew 'opencode'"):
			return "", "", errString("timed out after 10m0s")
		}
		return probe, "", nil
	})
	got, err := Upgrade(context.Background(), cfg, Options{Phase: PhaseApply}, fake)
	if err != nil {
		t.Fatal(err)
	}
	saw := map[string]Row{}
	for _, h := range got.Hosts {
		for _, row := range h.Rows {
			saw[row.Tool] = row
		}
	}
	if saw["codex"].Status != StApplied || saw["codex"].Failed {
		t.Fatalf("an apply that already reported success is not failed: %+v", saw["codex"])
	}
	if saw["opencode"].Status != StFailed || !strings.Contains(saw["opencode"].Detail, "timed out") {
		t.Fatalf("opencode %+v", saw["opencode"])
	}
	if !got.Failed() {
		t.Fatal("the timed-out apply should still fail the run")
	}
}

func TestInterruptedApplyIsNotSuccess(t *testing.T) {
	cfg := parseFleet(t, "[[hosts]]\nname = \"box\"\nssh = \"box\"\ntools = [\"codex\"]\n")
	probe := probeOut(withPkg(present("codex", "/usr/local/bin/codex", "/usr/local/lib/node_modules/@openai/codex/bin/codex", "0.1.0"), "@openai/codex"))
	for _, phase := range []string{PhaseApply, PhaseUpgrade} {
		fake := scriptRunner(func(script string) (string, string, error) {
			switch {
			case strings.Contains(script, "apply_npm 'codex'"):
				// The swap was reported; Ctrl-C landed during the re-probe.
				return "clifleet-apply/1\n" + actionLine("apply", "codex", "0", "swapped") + "\n", "", fmt.Errorf("%w; the remote step had started", runner.ErrInterrupted)
			case strings.Contains(script, "clifleet-fetch/1"):
				return "clifleet-fetch/1\n" + actionLine("fetch", "codex", "0", "staged") + "\n", "", nil
			}
			return probe, "", nil
		})
		got, err := Upgrade(context.Background(), cfg, Options{Phase: phase}, fake)
		if err != nil {
			t.Fatal(err)
		}
		h := got.Hosts[0]
		if h.Rows[0].Status != StApplied || !strings.Contains(h.Error, "interrupted") || !got.Failed() {
			t.Fatalf("%s: an interrupted run must not report success: error=%q rows=%+v", phase, h.Error, h.Rows)
		}
	}
}

func TestOneShotHostErrorKeepsFinishedRows(t *testing.T) {
	cfg := parseFleet(t, `
[[hosts]]
name = "box"
ssh = "box"
tools = ["claude", "codex"]
`)
	probe := probeOut(
		present("claude", "/home/a/.local/bin/claude", "/home/a/.local/bin/claude", "2.1.40"),
		withPkg(present("codex", "/usr/local/bin/codex", "/usr/local/lib/node_modules/@openai/codex/bin/codex", "0.1.0"), "@openai/codex"),
	)
	fake := scriptRunner(func(script string) (string, string, error) {
		if strings.Contains(script, "clifleet-upgrade/1") {
			// The connection dropped while codex's line was half written.
			return "clifleet-upgrade/1\n" + strings.Join([]string{"CF1", "upgrade", "claude", "0"}, "\t") + "\nCF1\tupgrade\tcodex\t", "", errString("timed out after 10m0s")
		}
		return probe, "", nil
	})
	got, err := Upgrade(context.Background(), cfg, Options{OneShot: true}, fake)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Failed() || got.Hosts[0].Error == "" {
		t.Fatalf("the host error must still fail the run: %s", got.Summary())
	}
	saw := map[string]Row{}
	for _, row := range got.Hosts[0].Rows {
		saw[row.Tool] = row
	}
	if saw["claude"].Status != StUpgraded || saw["claude"].Failed {
		t.Fatalf("claude finished before the timeout: %+v", saw["claude"])
	}
	if saw["codex"].Status != StFailed || !strings.Contains(saw["codex"].Detail, "timed out") {
		t.Fatalf("codex %+v", saw["codex"])
	}
}

// Ctrl-C during the probe is not an unreachable host.
func TestInterruptedProbeIsNotUnreachable(t *testing.T) {
	cfg := parseFleet(t, "[[hosts]]\nname = \"box\"\nssh = \"box\"\ntools = [\"claude\"]\n")
	fake := &fakeRunner{fail: map[string]error{"box": fmt.Errorf("%w before the remote step started", runner.ErrInterrupted)}}
	report, err := Status(context.Background(), cfg, Options{}, fake)
	if err != nil {
		t.Fatal(err)
	}
	row := report.Hosts[0].Rows[0]
	if row.Status == StUnreachable || row.Status != StFailed || !strings.Contains(row.Detail, "interrupted") || !report.Failed() {
		t.Fatalf("%+v", row)
	}
}

// brew upgrade can link the new keg and still exit non-zero; the reprobe
// decides. npm keeps its failure even when the version moved (rollback).
func TestBrewApplyThatMovedTheVersionIsApplied(t *testing.T) {
	cfg := parseFleet(t, "[[hosts]]\nname = \"box\"\nssh = \"box\"\ntools = [\"opencode\", \"codex\"]\n")
	brewLine := func(v string) string {
		return withBrew(present("opencode", "/opt/homebrew/bin/opencode", "/opt/homebrew/Cellar/opencode/"+v+"/bin/opencode", v), "formula", "opencode")
	}
	npmLine := func(v string) string {
		return withPkg(present("codex", "/usr/local/bin/codex", "/usr/local/lib/node_modules/@openai/codex/bin/codex", v), "@openai/codex")
	}
	run := func(opencodeAfter, codexAfter string) map[string]Row {
		t.Helper()
		fake := &fakeRunner{
			probe: map[string]string{"box": probeOut(brewLine("1.3.0"), npmLine("0.1.0"))},
			apply: map[string]string{"box": strings.Join([]string{
				"clifleet-apply/1",
				actionLine("apply", "opencode", "blocked", "Xcode is too old for Homebrew"),
				actionLine("apply", "codex", "1", "rollback failed"),
				"clifleet-probe/1",
				brewLine(opencodeAfter),
				npmLine(codexAfter),
			}, "\n") + "\n"},
		}
		got, err := Upgrade(context.Background(), cfg, Options{Phase: PhaseApply}, fake)
		if err != nil {
			t.Fatal(err)
		}
		return rowsByTool(got)
	}

	rows := run("1.4.0", "0.2.0")
	if r := rows["opencode"]; r.Status != StApplied || r.Version != "1.3.0 → 1.4.0" || r.Failed || !strings.Contains(r.Detail, "Xcode") {
		t.Fatalf("opencode %+v", r)
	}
	if r := rows["codex"]; r.Status != StFailed || !r.Failed {
		t.Fatalf("npm failure must stay a failure: %+v", r)
	}

	rows = run("1.3.0", "0.1.0")
	if r := rows["opencode"]; r.Status != StBlocked || r.Version != "1.3.0" {
		t.Fatalf("unchanged opencode %+v", r)
	}
}
