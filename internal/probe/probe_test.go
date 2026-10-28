package probe

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/indiekitai/clifleet/internal/catalog"
)

func TestProbeScriptIsReadOnly(t *testing.T) {
	script, err := ProbeScript([]Tool{{Name: "claude", Binary: "claude"}}, []string{"/opt/custom/bin"})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"curl", "wget", "npm ", "brew ", "apt-get", "pip "} {
		if strings.Contains(script, bad) {
			t.Fatalf("probe script contains %q", bad)
		}
	}
	if !strings.Contains(script, "</dev/null") {
		t.Fatal("version check does not detach stdin")
	}
}

func TestUpgradeScriptQuotesAndDetaches(t *testing.T) {
	script, err := UpgradeScriptSteps(
		[]Tool{{Name: "claude", Binary: "claude"}, {Name: "codex", Binary: "codex"}},
		nil,
		[]Step{{Tool: "claude", Cmd: catalog.Command{Argv: []string{"claude", "update"}, Method: "native"}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "'claude' 'update' </dev/null") {
		t.Fatalf("script missing quoted command:\n%s", script)
	}
	if strings.Contains(script, "'codex' 'update'") || strings.Contains(script, "@openai/codex") {
		t.Fatal("upgrade script runs a command for a tool that was not planned")
	}
	if strings.Contains(script, "curl") {
		t.Fatal("upgrade script curls")
	}
	if _, err := UpgradeScriptSteps(nil, nil, []Step{{Tool: "claude", Cmd: catalog.Command{Argv: []string{"ok\nrm"}}}}); err == nil {
		t.Fatal("newline arg should be rejected")
	}
}

func TestParseProbe(t *testing.T) {
	raw := strings.Join([]string{
		"clifleet-probe/1",
		strings.Join([]string{"CF1", "present", "claude", "/bin/claude", "/cellar/claude", "2.1.44 (Claude Code)", "cask", "claude-code", ""}, "\t"),
		strings.Join([]string{"CF1", "missing", "codex"}, "\t"),
		"some motd line",
	}, "\n")
	obs, err := ParseProbe(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !obs["claude"].Present || obs["claude"].BrewToken != "claude-code" || obs["claude"].BrewKind != "cask" {
		t.Fatalf("%+v", obs["claude"])
	}
	if obs["codex"].Present {
		t.Fatal("codex should be missing")
	}
	if ShortVersion(obs["claude"].Version) != "2.1.44" {
		t.Fatal(ShortVersion(obs["claude"].Version))
	}
	if _, err := ParseProbe("hello"); err == nil {
		t.Fatal("expected header error")
	}
}

func TestProbeScriptRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	bin := filepath.Join(root, "bin")
	cellar := filepath.Join(root, "Cellar", "opencode", "1.2.3", "bin")
	npm := filepath.Join(root, "node_modules", "@openai", "codex")
	if err := os.MkdirAll(cellar, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(npm, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExe(t, filepath.Join(cellar, "opencode"), "#!/bin/sh\nprintf '1.2.3\\n'\n")
	if err := os.Symlink(filepath.Join(cellar, "opencode"), filepath.Join(bin, "opencode")); err != nil {
		t.Fatal(err)
	}
	// This binary reads stdin. The probe must redirect it to /dev/null or
	// the rest of the script disappears.
	writeExe(t, filepath.Join(bin, "codex"), "#!/bin/sh\ncat >/dev/null\nif [ \"$1\" = \"--version\" ]; then printf 'codex-cli 0.163.0\\n'; fi\n")
	if err := os.WriteFile(filepath.Join(npm, "package.json"), []byte("{\n  \"name\": \"@openai/codex\"\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Point the codex symlink through node_modules by replacing the bin entry.
	if err := os.Remove(filepath.Join(bin, "codex")); err != nil {
		t.Fatal(err)
	}
	realCodex := filepath.Join(npm, "codex")
	writeExe(t, realCodex, "#!/bin/sh\ncat >/dev/null\nif [ \"$1\" = \"--version\" ]; then printf 'codex-cli 0.163.0\\n'; fi\n")
	if err := os.Symlink(realCodex, filepath.Join(bin, "codex")); err != nil {
		t.Fatal(err)
	}

	script, err := ProbeScript([]Tool{
		{Name: "opencode", Binary: "opencode"},
		{Name: "codex", Binary: "codex"},
		{Name: "claude", Binary: "claude"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-s")
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = []string{
		"PATH=" + bin + ":/usr/bin:/bin",
		"HOME=" + home,
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("probe failed: %v\n%s", err, out)
	}
	obs, err := ParseProbe(string(out))
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, out)
	}
	if !obs["opencode"].Present || obs["opencode"].BrewKind != "formula" || obs["opencode"].BrewToken != "opencode" {
		t.Fatalf("opencode %+v", obs["opencode"])
	}
	if ShortVersion(obs["opencode"].Version) != "1.2.3" {
		t.Fatalf("version %q", obs["opencode"].Version)
	}
	if !obs["codex"].Present || obs["codex"].Package != "@openai/codex" {
		t.Fatalf("codex %+v", obs["codex"])
	}
	if ShortVersion(obs["codex"].Version) != "0.163.0" {
		t.Fatalf("codex version %q", obs["codex"].Version)
	}
	if obs["claude"].Present {
		t.Fatal("claude should be missing")
	}
}

func writeExe(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}
