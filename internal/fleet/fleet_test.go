package fleet

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

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
			if line[3] != "brew" || !strings.Contains(line[4], "brew upgrade --cask claude-code") {
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
		present("claude", "/home/a/.local/bin/claude", "/home/a/.local/bin/claude", "2.1.40"),
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
	if !strings.Contains(saw["claude"], "no-stage") || !strings.Contains(saw["claude"], "native updater") {
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
			actionLine("fetch", "codex", "0", "/home/a/.cache/clifleet/npm/codex/openai-codex-9.9.9.tgz"),
			actionLine("fetch", "opencode", "0", "brew cache (formula opencode)"),
		}, "\n") + "\n"},
	}
	got, err := Upgrade(context.Background(), cfg, Options{Phase: PhaseFetch}, fake)
	if err != nil || got.Failed() {
		t.Fatalf("%v %s %v", err, got.Summary(), got.Errors())
	}
	if !strings.Contains(got.Summary(), "2 staged") || !strings.Contains(got.Summary(), "1 no-stage") {
		t.Fatal(got.Summary())
	}
	joined := strings.Join(fake.Scripts(), "\n---\n")
	if !strings.Contains(joined, "stage_npm 'codex' '@openai/codex'") || !strings.Contains(joined, "stage_brew 'opencode' 'formula' 'opencode'") {
		t.Fatalf("fetch script:\n%s", joined)
	}
	if strings.Contains(joined, "'claude' 'update'") || strings.Contains(joined, "npm install") || strings.Contains(joined, "stage_npm 'cursor'") {
		t.Fatalf("fetch touched a live or unstaged tool:\n%s", joined)
	}
	for _, h := range got.Hosts {
		for _, row := range h.Rows {
			switch row.Tool {
			case "codex":
				if row.Status != StStaged || !strings.Contains(row.Detail, "openai-codex-9.9.9.tgz") {
					t.Fatalf("codex %+v", row)
				}
			case "claude":
				if row.Status != StNoStage {
					t.Fatalf("claude %+v", row)
				}
			}
		}
	}

	fake = &fakeRunner{
		probe: map[string]string{"box": probe},
		apply: map[string]string{"box": strings.Join([]string{
			"clifleet-apply/1",
			actionLine("apply", "codex", "skip", "nothing staged"),
			actionLine("apply", "opencode", "0", "brew cache (formula opencode)"),
			"clifleet-probe/1",
			withBrew(present("opencode", "/opt/homebrew/bin/opencode", "/opt/homebrew/Cellar/opencode/1.3.0/bin/opencode", "1.3.0"), "formula", "opencode"),
			withPkg(present("codex", "/usr/local/bin/codex", "/usr/local/lib/node_modules/@openai/codex/bin/codex", "0.163.0"), "@openai/codex"),
			present("claude", "/home/a/.local/bin/claude", "/home/a/.local/bin/claude", "2.1.40"),
			present("cursor", "/usr/local/bin/cursor", "/Applications/Cursor.app/Contents/Resources/app/bin/cursor", "3.0.0"),
		}, "\n") + "\n"},
	}
	applied, err := Upgrade(context.Background(), cfg, Options{Phase: PhaseApply}, fake)
	if err != nil || applied.Failed() {
		t.Fatalf("apply should skip a missing stamp without failing the host: %v %s", err, applied.Summary())
	}
	if !strings.Contains(applied.Summary(), "1 applied") {
		t.Fatal(applied.Summary())
	}
	applyScript := strings.Join(fake.Scripts(), "\n")
	if strings.Contains(applyScript, "npm pack") || strings.Contains(applyScript, "@latest") || strings.Contains(applyScript, "brew fetch") {
		t.Fatalf("apply re-downloaded:\n%s", applyScript)
	}
	if !strings.Contains(applyScript, "apply_npm 'codex'") || !strings.Contains(applyScript, "apply_brew 'opencode'") {
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
				if row.Status != StNoStage {
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
	oneShot, err := Upgrade(context.Background(), cfg, Options{Only: []string{"claude"}}, fake)
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
	if !strings.Contains(line, "npm install -g --no-fund --no-audit") || !strings.Contains(line, "<staged>.tgz") || strings.Contains(line, "@latest") {
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
	mu      sync.Mutex
	scripts []string
	probe   map[string]string
	upgrade map[string]string
	fetch   map[string]string
	apply   map[string]string
	fail    map[string]error
}

func (f *fakeRunner) Run(_ context.Context, target runner.Target, script string) (string, string, error) {
	f.mu.Lock()
	f.scripts = append(f.scripts, target.Name+"\n"+script)
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
