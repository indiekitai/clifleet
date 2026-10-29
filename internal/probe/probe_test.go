package probe

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/indiekitai/clifleet/internal/catalog"
)

func TestProbeScriptIsReadOnly(t *testing.T) {
	script, err := ProbeScript([]Tool{{Name: "claude", Binary: "claude"}}, []string{"/opt/custom/bin"})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"curl", "wget", "npm ", "brew install", "brew update", "brew upgrade", "brew fetch", "apt-get", "pip "} {
		if strings.Contains(script, bad) {
			t.Fatalf("probe script contains %q", bad)
		}
	}
	if !strings.Contains(script, "/opt/homebrew/bin") || !strings.Contains(script, "/usr/local/bin") || !strings.Contains(script, "shellenv") {
		t.Fatal("probe script does not set up Homebrew the way a login shell does")
	}
	if strings.LastIndex(script, "cf_ensure_path") < strings.Index(script, "nvm.sh") {
		t.Fatal("homebrew paths are not re-applied after nvm")
	}
	if !strings.Contains(script, "</dev/null") {
		t.Fatal("version check does not detach stdin")
	}
	if strings.Contains(script, `. "$HOME/.nvm/nvm.sh" >/dev/null 2>&1 || true`) {
		t.Fatal("nvm is sourced on the probe stdin")
	}
	if !strings.Contains(script, "cf_cap_both 15") || !strings.Contains(script, "cf_cap 5") || !strings.Contains(script, "clifleet-script/1") {
		t.Fatal("probe caps or script marker missing")
	}
	if strings.Contains(script, "cf_cap 3") {
		t.Fatal("version probe is still capped at 3s")
	}
	if strings.Contains(script, `prepend "$HOME/.local/share/mise/shims"`) {
		t.Fatal("mise shims are prepended ahead of real binaries")
	}
	if !strings.Contains(script, `append_missing "$HOME/.local/share/mise/shims"`) {
		t.Fatal("mise shims are not on PATH at all")
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

func TestVersionCapDoesNotBlockTheHost(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExe(t, filepath.Join(bin, "claude"), "#!/bin/sh\nsleep 30\n")
	writeExe(t, filepath.Join(bin, "codex"), "#!/bin/sh\nprintf 'codex-cli 0.1.0\\n'\n")
	script, err := ProbeScript([]Tool{{Name: "claude", Binary: "claude"}, {Name: "codex", Binary: "codex"}}, []string{bin})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	cmd := exec.Command("sh", "-s")
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + filepath.Join(root, "home")}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("probe failed: %v\n%s", err, out)
	}
	if time.Since(start) > 22*time.Second {
		t.Fatalf("version cap took %s", time.Since(start))
	}
	obs, err := ParseProbe(string(out))
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, out)
	}
	if !obs["claude"].Present || obs["claude"].Version != "version timed out" {
		t.Fatalf("claude %+v", obs["claude"])
	}
	if !obs["codex"].Present || ShortVersion(obs["codex"].Version) != "0.1.0" {
		t.Fatalf("codex %+v", obs["codex"])
	}
}

func TestNvmCapDoesNotBlockTheHost(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	bin := filepath.Join(root, "bin")
	nvmBin := filepath.Join(home, "nvmbin")
	if err := os.MkdirAll(filepath.Join(home, ".nvm"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(nvmBin, 0o755); err != nil || os.MkdirAll(bin, 0o755) != nil {
		t.Fatal(err)
	}
	// Healthy nvm only adjusts PATH. The binary is not on the initial PATH.
	if err := os.WriteFile(filepath.Join(home, ".nvm", "nvm.sh"), []byte("PATH=\""+nvmBin+":$PATH\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeExe(t, filepath.Join(nvmBin, "claude"), "#!/bin/sh\nprintf '2.0.0\\n'\n")
	script, err := ProbeScript([]Tool{{Name: "claude", Binary: "claude"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-s")
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + home}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("probe failed: %v\n%s", err, out)
	}
	obs, err := ParseProbe(string(out))
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, out)
	}
	if !obs["claude"].Present || ShortVersion(obs["claude"].Version) != "2.0.0" {
		t.Fatalf("nvm PATH was not applied: %+v\n%s", obs["claude"], out)
	}

	// A stuck nvm.sh must not consume the session. The CLI on the static PATH still reports.
	if err := os.WriteFile(filepath.Join(home, ".nvm", "nvm.sh"), []byte("sleep 30\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeExe(t, filepath.Join(bin, "claude"), "#!/bin/sh\nprintf '1.0.0\\n'\n")
	start := time.Now()
	cmd = exec.Command("sh", "-s")
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + home}
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("stuck nvm: %v\n%s", err, out)
	}
	if time.Since(start) > 12*time.Second {
		t.Fatalf("nvm cap took %s", time.Since(start))
	}
	obs, err = ParseProbe(string(out))
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, out)
	}
	if !obs["claude"].Present || ShortVersion(obs["claude"].Version) != "1.0.0" {
		t.Fatalf("static PATH claude %+v", obs["claude"])
	}
}

func TestLoginPathSurvivesNvmAndBrewShellenv(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	linuxbrew := filepath.Join(home, ".linuxbrew", "bin")
	if err := os.MkdirAll(filepath.Join(home, ".nvm"), 0o755); err != nil || os.MkdirAll(linuxbrew, 0o755) != nil {
		t.Fatal(err)
	}
	// nvm replaces PATH with the ssh default and drops the directories we prepended.
	if err := os.WriteFile(filepath.Join(home, ".nvm", "nvm.sh"), []byte("PATH=/usr/bin:/bin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeExe(t, filepath.Join(linuxbrew, "claude"), "#!/bin/sh\nprintf '2.4.0\\n'\n")
	script, err := ProbeScript([]Tool{{Name: "claude", Binary: "claude"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-s")
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("probe failed: %v\n%s", err, out)
	}
	obs, err := ParseProbe(string(out))
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, out)
	}
	if !obs["claude"].Present || ShortVersion(obs["claude"].Version) != "2.4.0" {
		t.Fatalf("brew path was dropped after nvm: %+v\n%s", obs["claude"], out)
	}

	// Login shells also eval brew shellenv. A binary that is only on the
	// prefix shellenv adds must still be visible.
	brewonly := filepath.Join(home, "brewonly")
	if err := os.MkdirAll(brewonly, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExe(t, filepath.Join(linuxbrew, "brew"), "#!/bin/sh\nif [ \"$1\" = shellenv ]; then\n  printf 'export HOMEBREW_PREFIX=\"%s\"\\n' \"$HOME/.linuxbrew\"\n  printf 'export PATH=\"%s/brewonly:$PATH\"\\n' \"$HOME\"\n  exit 0\nfi\nexit 1\n")
	if err := os.Remove(filepath.Join(linuxbrew, "claude")); err != nil {
		t.Fatal(err)
	}
	writeExe(t, filepath.Join(brewonly, "claude"), "#!/bin/sh\nprintf '9.9.9\\n'\n")
	if err := os.Remove(filepath.Join(home, ".nvm", "nvm.sh")); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("sh", "-s")
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home}
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("shellenv probe: %v\n%s", err, out)
	}
	obs, err = ParseProbe(string(out))
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, out)
	}
	if !obs["claude"].Present || ShortVersion(obs["claude"].Version) != "9.9.9" {
		t.Fatalf("brew shellenv was not applied: %+v\n%s", obs["claude"], out)
	}
}

func TestBrewListVersionWhenBinaryPrintsNothing(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	cellar := filepath.Join(root, "Cellar", "opencode", "1.2.3", "bin")
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(cellar, 0o755); err != nil || os.MkdirAll(bin, 0o755) != nil || os.MkdirAll(home, 0o755) != nil {
		t.Fatal(err)
	}
	writeExe(t, filepath.Join(cellar, "opencode"), "#!/bin/sh\nexit 1\n")
	writeExe(t, filepath.Join(bin, "brew"), "#!/bin/sh\nif [ \"$1\" = list ]; then printf 'opencode 1.2.3\\n'; exit 0; fi\nexit 1\n")
	script, err := ProbeScript([]Tool{{Name: "opencode", Binary: "opencode"}}, []string{cellar, bin})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-s")
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("probe failed: %v\n%s", err, out)
	}
	obs, err := ParseProbe(string(out))
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, out)
	}
	if ShortVersion(obs["opencode"].Version) != "1.2.3" {
		t.Fatalf("brew list version was not used: %+v\n%s", obs["opencode"], out)
	}
}

func writeExe(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}
