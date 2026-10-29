package probe

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/indiekitai/clifleet/internal/catalog"
)

func TestProbeScriptIsReadOnly(t *testing.T) {
	withProductionPaths(t)
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

// brew shellenv prepends its own bin. A path_extra directory the host asked
// for must still win, or probe and apply act on Homebrew's copy instead.
func TestPathExtraWinsOverBrewShellenv(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	linuxbrew := filepath.Join(home, ".linuxbrew", "bin")
	brewonly := filepath.Join(home, "brewonly")
	extra := filepath.Join(root, "extra")
	for _, d := range []string{linuxbrew, brewonly, extra} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeExe(t, filepath.Join(linuxbrew, "brew"), "#!/bin/sh\nif [ \"$1\" = shellenv ]; then\n  printf 'export PATH=\"%s/brewonly:$PATH\"\\n' \"$HOME\"\n  exit 0\nfi\nexit 1\n")
	writeExe(t, filepath.Join(brewonly, "claude"), "#!/bin/sh\nprintf '1.0.0\\n'\n")
	writeExe(t, filepath.Join(extra, "claude"), "#!/bin/sh\nprintf '2.0.0\\n'\n")
	script, err := ProbeScript([]Tool{{Name: "claude", Binary: "claude"}}, []string{extra})
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
	if obs["claude"].Path != filepath.Join(extra, "claude") {
		t.Fatalf("path_extra lost to brew shellenv: %+v\n%s", obs["claude"], out)
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

// A capped shell function runs its work in child processes. When the cap
// fires, those children must die with it instead of running on as orphans.
func TestCapKillsTheWholeTree(t *testing.T) {
	if out, err := exec.Command("ps", "-A", "-o", "pid=").Output(); err != nil || len(out) == 0 {
		// macOS sandbox-exec refuses to run /bin/ps; real hosts and CI have it.
		t.Skip("ps is not usable here")
	}
	root := t.TempDir()
	body := scriptFns + `
cf_dir=` + root + `
slow() {
  sleep 317 &
  printf '%s\n' "$!" >"$cf_dir/kid"
  wait
}
cf_limit 1 slow
printf 'rc=%s\n' "$?"
kid=$(cat "$cf_dir/kid")
sleep 1
if kill -0 "$kid" 2>/dev/null; then
  kill -9 "$kid"
  echo orphan
else
  echo gone
fi
cf_cap 1 "$cf_dir/out" slow
printf 'rc=%s\n' "$?"
kid=$(cat "$cf_dir/kid")
sleep 1
if kill -0 "$kid" 2>/dev/null; then
  kill -9 "$kid"
  echo orphan
else
  echo gone
fi
# A child that ignores TERM outlives its parent; the KILL must still reach it.
stubborn() {
  ( trap '' TERM; exec sleep 317 ) &
  printf '%s\n' "$!" >"$cf_dir/kid"
  wait
}
cf_limit 1 stubborn
printf 'rc=%s\n' "$?"
kid=$(cat "$cf_dir/kid")
sleep 1
if kill -0 "$kid" 2>/dev/null; then
  kill -9 "$kid"
  echo orphan
else
  echo gone
fi
# A PID is matched with its start time, so a reused PID is not.
sleep 317 &
other=$!
case $(cf_tree "$other:_not_its_start") in
  '') echo identity-ok ;;
  *) echo identity-ignored ;;
esac
case $(cf_tree "$other:*") in
  "$other:"*) echo tree-ok ;;
  *) echo tree-missed ;;
esac
kill -9 "$other"
`
	out := runSh(t, body, []string{"PATH=/usr/bin:/bin", "HOME=" + root})
	if strings.Count(out, "rc=124") != 3 || strings.Contains(out, "orphan") || strings.Count(out, "gone") != 3 ||
		!strings.Contains(out, "identity-ok") || !strings.Contains(out, "tree-ok") {
		t.Fatalf("cap left a child running:\n%s", out)
	}
}

// A path with a tab would shift every field after it; one with a newline would
// split the line. The tool gets no result (it fails alone), the others still parse.
func TestProbeSkipsPathWithTabOrNewline(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	odd := filepath.Join(root, "b\tin")
	for _, dir := range []string{bin, odd} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// path_extra rejects such a directory, but a symlink can still resolve into one.
	writeExe(t, filepath.Join(odd, "claude"), "#!/bin/sh\nprintf '2.1.40\\n'\n")
	if err := os.Symlink(filepath.Join(odd, "claude"), filepath.Join(bin, "claude")); err != nil {
		t.Fatal(err)
	}
	writeExe(t, filepath.Join(bin, "codex"), "#!/bin/sh\nprintf 'codex-cli 0.1.0\\n'\n")
	script, err := ProbeScript([]Tool{{Name: "claude", Binary: "claude"}, {Name: "codex", Binary: "codex"}}, []string{bin})
	if err != nil {
		t.Fatal(err)
	}
	out := runSh(t, script, []string{"PATH=/usr/bin:/bin", "HOME=" + filepath.Join(root, "home")})
	obs, err := ParseProbe(out)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, out)
	}
	if _, ok := obs["claude"]; ok {
		t.Fatalf("claude under a tab path was reported: %+v", obs["claude"])
	}
	if !obs["codex"].Present || ShortVersion(obs["codex"].Version) != "0.1.0" {
		t.Fatalf("codex %+v\n%s", obs["codex"], out)
	}
	if !strings.Contains(out, "tab or newline") {
		t.Fatalf("no reason for the skipped tool:\n%s", out)
	}
}

func TestParseProbeRejectsShiftedFields(t *testing.T) {
	line := strings.Join([]string{"CF1", "present", "claude", "/a\tb/claude", "/a\tb/claude", "2.1.40", "", "", ""}, "\t")
	if _, err := ParseProbe("clifleet-probe/1\n" + line + "\n"); err == nil {
		t.Fatal("a present line with extra fields was accepted")
	}
}

// mawk 1.3.3, still the default awk on older Debian/Ubuntu, has no {n}
// intervals; a regex with one silently never matches.
func TestShellAwkHasNoRegexIntervals(t *testing.T) {
	interval := regexp.MustCompile(`[\])][{][0-9]+(,[0-9]*)?[}]`)
	for name, src := range map[string]string{
		"scriptPrelude": scriptPrelude, "scriptFns": scriptFns, "brewEnvSnippet": brewEnvSnippet, "nvmSnippet": nvmSnippet,
		"applySafeShell": applySafeShell, "codexPackShell": codexPackShell,
		"stagePrelude": stagePrelude, "fetchFns": fetchFns, "applyFns": applyFns,
	} {
		if m := interval.FindString(src); m != "" {
			t.Errorf("%s uses a regex interval %q", name, m)
		}
	}
}

func TestManifestChecksumWithoutIntervals(t *testing.T) {
	root := t.TempDir()
	want := strings.Repeat("ab", 32)
	manifest := filepath.Join(root, "manifest.json")
	body := "{\n  \"platforms\": {\n    \"linux-x64\": {\n      \"checksum\": \"" + want + "\",\n      \"size\": 1\n    }\n  }\n}\n"
	if err := os.WriteFile(manifest, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	awks := []string{"awk"}
	if _, err := exec.LookPath("mawk"); err == nil {
		awks = append(awks, "mawk")
	}
	for _, awk := range awks {
		script := releaseRest + "\nawk() { command " + awk + " \"$@\"; }\ncf_manifest_checksum '" + manifest + "' linux-x64\n"
		out := strings.TrimSpace(runSh(t, script, []string{"PATH=/usr/bin:/bin", "HOME=" + root}))
		if out != want {
			t.Fatalf("%s checksum %q", awk, out)
		}
	}
}

// A PID that changed hands between the TERM and the KILL is someone else's.
// A stand-in ps reports the process under a new start time on the second
// look, as the system would after reusing the PID.
func TestKillTreeSkipsAReusedPID(t *testing.T) {
	root := t.TempDir()
	body := scriptFns + `
cf_dir=` + root + `
sleep 317 &
r=$!
# Double fork: init reaps x once it dies, so kill -0 cannot see a zombie.
( ( trap '' TERM; exec sleep 317 ) & printf '%s\n' "$!" >"$cf_dir/x" )
x=$(cat "$cf_dir/x")
ps() {
  n=$(cat "$cf_dir/psn" 2>/dev/null || echo 0)
  n=$((n + 1))
  printf '%s\n' "$n" >"$cf_dir/psn"
  if [ "$n" -eq 1 ]; then
    printf '%s %s %s\n' "$r" 1 "Thu Oct 29 10:00:00 2026" "$x" "$r" "Thu Oct 29 10:00:01 2026"
  else
    printf '%s %s %s\n' "$x" 1 "Thu Oct 29 10:00:05 2026"
  fi
}
cf_kill_tree "$r"
sleep 1
if kill -0 "$x" 2>/dev/null; then echo spared; else echo killed; fi
# Without start times (BusyBox) a bare PID is not trusted for the KILL either.
rm -f "$cf_dir/psn"
ps() {
  printf '%s %s\n' "$r" 1 "$x" "$r"
}
cf_kill_tree "$r"
sleep 1
if kill -0 "$x" 2>/dev/null; then echo spared; else echo killed; fi
kill -9 "$x" 2>/dev/null
wait 2>/dev/null
`
	out := runSh(t, body, []string{"PATH=/usr/bin:/bin", "HOME=" + root})
	if strings.Count(out, "spared") != 2 {
		t.Fatalf("KILL reached a process that only reused a PID:\n%s", out)
	}
}
