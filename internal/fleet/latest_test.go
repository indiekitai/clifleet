package fleet

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/indiekitai/clifleet/internal/latest"
)

type fakeLatest struct {
	mu   sync.Mutex
	vers map[latest.Query]string
	asks []latest.Query
}

func (f *fakeLatest) Latest(_ context.Context, q latest.Query) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asks = append(f.asks, q)
	return f.vers[q]
}

const latestFleet = `
[[hosts]]
name = "box"
ssh = "box"
tools = ["agy", "codex", "claude", "pi"]
`

func npmLine(tool, pkg, version string) string {
	return withPkg(present(tool, "/usr/local/bin/"+tool, "/usr/local/lib/node_modules/"+pkg+"/bin/"+tool, version), pkg)
}

func latestProbe(codexVersion string) string {
	return probeOut(
		present("agy", "/home/a/.local/bin/agy", "/home/a/.local/bin/agy", "1.0.0"),
		npmLine("codex", "@openai/codex", codexVersion),
		npmLine("claude", "@anthropic-ai/claude-code", "2.1.296"),
		missing("pi"),
	)
}

func newestVersions() *fakeLatest {
	return &fakeLatest{vers: map[latest.Query]string{
		{Kind: latest.NPM, Key: "@openai/codex"}:             "0.165.0",
		{Kind: latest.NPM, Key: "@anthropic-ai/claude-code"}: "2.1.312",
	}}
}

func rowsByTool(r Report) map[string]Row {
	out := map[string]Row{}
	for _, h := range r.Hosts {
		for _, row := range h.Rows {
			out[row.Tool] = row
		}
	}
	return out
}

func TestFetchSkipsToolsAlreadyOnLatest(t *testing.T) {
	cfg := parseFleet(t, latestFleet)
	fake := &fakeRunner{
		probe: map[string]string{"box": latestProbe("0.165.0")},
		fetch: map[string]string{"box": "clifleet-fetch/1\n" + actionLine("fetch", "claude", "0", "/cache/claude.tgz") + "\n"},
	}
	newest := newestVersions()
	got, err := Upgrade(context.Background(), cfg, Options{Phase: PhaseFetch, Latest: newest.Latest}, fake)
	if err != nil || got.Failed() {
		t.Fatalf("%v %s %v", err, got.Summary(), got.Errors())
	}
	joined := strings.Join(fake.Scripts(), "\n")
	if strings.Contains(joined, "stage_npm 'codex'") || !strings.Contains(joined, "stage_npm 'claude'") {
		t.Fatalf("fetch should skip codex and stage claude:\n%s", joined)
	}
	rows := rowsByTool(got)
	if r := rows["codex"]; r.Status != StCurrent || r.Latest != "0.165.0" || r.Method != "npm" {
		t.Fatalf("codex %+v", r)
	}
	if r := rows["claude"]; r.Status != StStaged || r.Latest != "2.1.312" {
		t.Fatalf("claude %+v", r)
	}
	if r := rows["agy"]; r.Status != StNoStage || r.Latest != "" {
		t.Fatalf("agy %+v", r)
	}
	for _, q := range newest.asks {
		if q.Key == "@mariozechner/pi-coding-agent" || q.Key == "" {
			t.Fatalf("asked about a tool that is not installed or has no channel: %+v", newest.asks)
		}
	}
	if h := got.Header(); len(h) != 6 || h[3] != "LATEST" || len(got.Lines()[0]) != 6 {
		t.Fatalf("header %v line %v", h, got.Lines()[0])
	}

	// Same run as a dry run: codex is shown as up to date, not as would-fetch.
	dry, err := Upgrade(context.Background(), cfg, Options{Phase: PhaseFetch, DryRun: true, Latest: newest.Latest}, &fakeRunner{probe: fake.probe})
	if err != nil {
		t.Fatal(err)
	}
	if r := rowsByTool(dry)["codex"]; r.Status != StCurrent {
		t.Fatalf("dry-run codex %+v", r)
	}
}

func TestDefaultUpgradeSkipsCurrentToolsEntirely(t *testing.T) {
	cfg := parseFleet(t, latestFleet)
	newest := newestVersions()
	newest.vers[latest.Query{Kind: latest.NPM, Key: "@anthropic-ai/claude-code"}] = "2.1.296"
	fake := &fakeRunner{probe: map[string]string{"box": latestProbe("0.165.0")}}
	got, err := Upgrade(context.Background(), cfg, Options{Latest: newest.Latest}, fake)
	if err != nil || got.Failed() {
		t.Fatalf("%v %s %v", err, got.Summary(), got.Errors())
	}
	if n := len(fake.Scripts()); n != 1 {
		t.Fatalf("want only the probe, got %d scripts:\n%s", n, strings.Join(fake.Scripts(), "\n"))
	}
	if !strings.Contains(got.Summary(), "2 up to date") {
		t.Fatal(got.Summary())
	}
}

func TestFetchStillRunsWhenItCannotTell(t *testing.T) {
	cfg := parseFleet(t, latestFleet)
	cases := map[string]struct {
		codexVersion string
		newest       string
	}{
		"lookup failed":      {"0.165.0", ""},
		"older installed":    {"0.162.1", "0.165.0"},
		"version unreadable": {"version timed out", "0.165.0"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			newest := newestVersions()
			newest.vers[latest.Query{Kind: latest.NPM, Key: "@openai/codex"}] = c.newest
			fake := &fakeRunner{probe: map[string]string{"box": latestProbe(c.codexVersion)}}
			if _, err := Upgrade(context.Background(), cfg, Options{Phase: PhaseFetch, Latest: newest.Latest}, fake); err != nil {
				t.Fatal(err)
			}
			if joined := strings.Join(fake.Scripts(), "\n"); !strings.Contains(joined, "stage_npm 'codex'") {
				t.Fatalf("codex was not fetched:\n%s", joined)
			}
		})
	}
}

func TestApplyIgnoresLatest(t *testing.T) {
	cfg := parseFleet(t, latestFleet)
	fake := &fakeRunner{probe: map[string]string{"box": latestProbe("0.165.0")}}
	if _, err := Upgrade(context.Background(), cfg, Options{Phase: PhaseApply, Latest: newestVersions().Latest}, fake); err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(fake.Scripts(), "\n"); !strings.Contains(joined, "apply_npm 'codex'") {
		t.Fatalf("apply skipped a current tool:\n%s", joined)
	}
}

func TestStatusShowsLatest(t *testing.T) {
	cfg := parseFleet(t, latestFleet)
	fake := &fakeRunner{probe: map[string]string{"box": latestProbe("0.165.0")}}
	got, err := Status(context.Background(), cfg, Options{Latest: newestVersions().Latest}, fake)
	if err != nil || got.Failed() {
		t.Fatalf("%v %s", err, got.Summary())
	}
	rows := rowsByTool(got)
	if rows["codex"].Status != StCurrent || rows["claude"].Status != StBehind || rows["agy"].Status != StInstalled {
		t.Fatalf("%+v", rows)
	}
	if s := got.Summary(); !strings.Contains(s, "3 installed · 1 update available · 1 not installed") {
		t.Fatal(s)
	}
	if n := len(fake.Scripts()); n != 1 {
		t.Fatalf("status ran %d scripts", n)
	}

	plain, err := Status(context.Background(), cfg, Options{}, &fakeRunner{probe: fake.probe})
	if err != nil {
		t.Fatal(err)
	}
	if h := plain.Header(); len(h) != 5 || rowsByTool(plain)["claude"].Status != StInstalled {
		t.Fatalf("without lookups: %v %+v", h, rowsByTool(plain)["claude"])
	}
}

func TestLatestStatus(t *testing.T) {
	cases := []struct{ installed, newest, want string }{
		{"2.1.312", "2.1.312", StCurrent},
		{"2.1.296", "2.1.312", StBehind},
		{"0.2.1-rc.2", "0.2.0", ""},
		{"2026.10.22-27dcf5f", "2026.10.22-27dcf5f", StCurrent},
		{"2026.10.01-aaaaaaa", "2026.10.22-27dcf5f", StBehind},
		{"unknown", "1.0.0", ""},
		{"1.0.0", "", ""},
	}
	for _, c := range cases {
		if got := latestStatus(c.installed, c.newest); got != c.want {
			t.Errorf("latestStatus(%q, %q) = %q, want %q", c.installed, c.newest, got, c.want)
		}
	}
}
