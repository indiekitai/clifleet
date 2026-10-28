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
	fail    map[string]error
}

func (f *fakeRunner) Run(_ context.Context, target runner.Target, script string) (string, string, error) {
	f.mu.Lock()
	f.scripts = append(f.scripts, target.Name+"\n"+script)
	f.mu.Unlock()
	if err := f.fail[target.Name]; err != nil {
		return "", err.Error(), err
	}
	if strings.Contains(script, "clifleet-upgrade/1") {
		return f.upgrade[target.Name], "", nil
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
