package probe

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/indiekitai/clifleet/internal/catalog"
)

func TestStageScriptsStayInTheirPhase(t *testing.T) {
	npmStep := StageStep{Tool: "codex", Kind: catalog.MethodNpm, Package: "@openai/codex"}
	brewStep := StageStep{Tool: "claude", Kind: catalog.MethodBrew, BrewKind: "cask", BrewToken: "claude-code"}
	formula := StageStep{Tool: "opencode", Kind: catalog.MethodBrew, BrewKind: "formula", BrewToken: "opencode"}

	fetch, err := FetchScript(nil, []StageStep{npmStep, brewStep, formula})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"clifleet-fetch/1",
		"stage_npm 'codex' '@openai/codex'",
		"npm pack --loglevel=error --pack-destination",
		"stage_brew 'claude' 'cask' 'claude-code'",
		"stage_brew 'opencode' 'formula' 'opencode'",
		"brew update",
		"brew fetch --cask",
		"brew fetch --formula",
		"unset HOMEBREW_NO_AUTO_UPDATE",
	} {
		if !strings.Contains(fetch, want) {
			t.Fatalf("fetch script missing %q", want)
		}
	}
	for _, bad := range []string{"npm install", "brew upgrade", "brew install", "'claude' 'update'", "codex update", "@latest@latest"} {
		if strings.Contains(fetch, bad) {
			t.Fatalf("fetch script contains %q", bad)
		}
	}
	if strings.Count(fetch, "brew update </dev/null") != 1 {
		t.Fatalf("brew update should run from one place, got %d", strings.Count(fetch, "brew update </dev/null"))
	}

	apply, err := ApplyScript(
		[]Tool{{Name: "codex", Binary: "codex"}, {Name: "claude", Binary: "claude"}},
		nil,
		[]StageStep{npmStep, brewStep},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"clifleet-apply/1",
		"apply_npm 'codex'",
		"npm install -g --no-fund --no-audit",
		"apply_brew 'claude'",
		"brew upgrade --cask",
		"HOMEBREW_NO_AUTO_UPDATE=1",
		"clifleet-probe/1",
		"probe_one 'codex' 'codex'",
	} {
		if !strings.Contains(apply, want) {
			t.Fatalf("apply script missing %q", want)
		}
	}
	for _, bad := range []string{"npm pack", "@latest", "brew fetch", "brew update", "brew install", "'opencode'"} {
		if strings.Contains(apply, bad) {
			t.Fatalf("apply script contains %q", bad)
		}
	}

	if _, err := FetchScript(nil, []StageStep{{Tool: "codex", Kind: catalog.MethodNpm, Package: "-rf"}}); err == nil {
		t.Fatal("unsafe package should be rejected")
	}
	if _, err := FetchScript(nil, []StageStep{{Tool: "claude", Kind: catalog.MethodBrew, BrewKind: "cask", BrewToken: "claude;rm"}}); err == nil {
		t.Fatal("unsafe brew token should be rejected")
	}
	if _, err := ApplyScript(nil, nil, []StageStep{npmStep}); err == nil {
		t.Fatal("apply with no reprobe tools should fail")
	}
}

func TestFetchApplyNpmRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	cache := filepath.Join(root, "xdg")
	bin := filepath.Join(root, "bin")
	logPath := filepath.Join(root, "npm.log")
	if err := os.MkdirAll(home, 0o755); err != nil || os.MkdirAll(bin, 0o755) != nil {
		t.Fatal(err)
	}
	writeExe(t, filepath.Join(bin, "npm"), `#!/bin/sh
printf '%s\n' "$*" >> "$NPM_LOG"
if [ "$1" = pack ]; then
  dest=.
  prev=
  for a in "$@"; do
    if [ "$prev" = --pack-destination ]; then
      dest=$a
    fi
    prev=$a
  done
  mkdir -p "$dest"
  printf 'packed\n' > "$dest/openai-codex-9.9.9.tgz"
  printf '%s\n' "openai-codex-9.9.9.tgz"
  exit 0
fi
if [ "$1" = install ]; then
  exit 0
fi
echo "unexpected npm $*" >&2
exit 1
`)
	// A stand-in for the live binary. Fetch must not rewrite it.
	live := filepath.Join(bin, "codex")
	writeExe(t, live, "#!/bin/sh\nprintf 'codex-cli 0.1.0\\n'\n")

	step := StageStep{Tool: "codex", Kind: catalog.MethodNpm, Package: "@openai/codex"}
	fetch, err := FetchScript([]string{bin}, []StageStep{step})
	if err != nil {
		t.Fatal(err)
	}
	env := []string{
		"PATH=" + bin + ":/usr/bin:/bin",
		"HOME=" + home,
		"XDG_CACHE_HOME=" + cache,
		"NPM_LOG=" + logPath,
	}
	out := runSh(t, fetch, env)
	results, err := ParseFetch(out)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, out)
	}
	if len(results) != 1 || results[0].Exit != 0 || results[0].Skip != "" {
		t.Fatalf("fetch result %+v\n%s", results, out)
	}
	staged := filepath.Join(cache, "clifleet", "npm", "codex", "openai-codex-9.9.9.tgz")
	if results[0].Detail != staged {
		t.Fatalf("detail %q", results[0].Detail)
	}
	body, err := os.ReadFile(staged)
	if err != nil || string(body) != "packed\n" {
		t.Fatalf("tarball %v %q", err, body)
	}
	if _, err := os.Stat(filepath.Join(home, ".cache", "clifleet")); err == nil {
		t.Fatal("fetch wrote ~/.cache even though XDG_CACHE_HOME was set")
	}
	liveBody, err := os.ReadFile(live)
	if err != nil || !strings.Contains(string(liveBody), "0.1.0") {
		t.Fatal("fetch replaced the live binary")
	}

	apply, err := ApplyScript([]Tool{{Name: "codex", Binary: "codex"}}, []string{bin}, []StageStep{step})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	out = runSh(t, apply, env)
	applied, obs, err := ParseApply(out)
	if err != nil {
		t.Fatalf("parse apply: %v\n%s", err, out)
	}
	if len(applied) != 1 || applied[0].Exit != 0 || applied[0].Detail != staged {
		t.Fatalf("apply %+v", applied)
	}
	if !obs["codex"].Present || ShortVersion(obs["codex"].Version) != "0.1.0" {
		t.Fatalf("reprobe %+v", obs["codex"])
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "install -g --no-fund --no-audit "+staged) {
		t.Fatalf("npm install args:\n%s", log)
	}
	if strings.Contains(string(log), "@latest") || strings.Contains(string(log), "pack") {
		t.Fatalf("apply should be local I/O:\n%s", log)
	}
}

func TestApplySkipsWhenNothingStaged(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	logPath := filepath.Join(root, "npm.log")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExe(t, filepath.Join(bin, "npm"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$NPM_LOG\"\nexit 0\n")
	writeExe(t, filepath.Join(bin, "codex"), "#!/bin/sh\nprintf '1.0.0\\n'\n")
	script, err := ApplyScript([]Tool{{Name: "codex", Binary: "codex"}}, []string{bin}, []StageStep{{
		Tool: "codex", Kind: catalog.MethodNpm, Package: "@openai/codex",
	}})
	if err != nil {
		t.Fatal(err)
	}
	out := runSh(t, script, []string{
		"PATH=" + bin + ":/usr/bin:/bin",
		"HOME=" + filepath.Join(root, "home"),
		"NPM_LOG=" + logPath,
	})
	results, _, err := ParseApply(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(results) != 1 || results[0].Skip != "nothing staged" {
		t.Fatalf("%+v\n%s", results, out)
	}
	if _, err := os.Stat(logPath); err == nil {
		body, _ := os.ReadFile(logPath)
		if len(body) > 0 {
			t.Fatalf("npm ran with nothing staged:\n%s", body)
		}
	}
}

func TestApplyRejectsTarballOutsideCache(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	bin := filepath.Join(root, "bin")
	logPath := filepath.Join(root, "npm.log")
	outside := filepath.Join(root, "evil.tgz")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("nope\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeExe(t, filepath.Join(bin, "npm"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$NPM_LOG\"\nexit 0\n")
	writeExe(t, filepath.Join(bin, "codex"), "#!/bin/sh\nprintf '1.0.0\\n'\n")
	stampDir := filepath.Join(home, ".cache", "clifleet", "staged")
	if err := os.MkdirAll(stampDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stampDir, "codex"), []byte("npm\t@openai/codex\t"+outside+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(home, ".cache", "clifleet", "npm", "codex")
	if err := os.MkdirAll(linkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script, err := ApplyScript([]Tool{{Name: "codex", Binary: "codex"}}, []string{bin}, []StageStep{{
		Tool: "codex", Kind: catalog.MethodNpm, Package: "@openai/codex",
	}})
	if err != nil {
		t.Fatal(err)
	}
	env := []string{
		"PATH=" + bin + ":/usr/bin:/bin",
		"HOME=" + home,
		"NPM_LOG=" + logPath,
	}
	out := runSh(t, script, env)
	results, _, err := ParseApply(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(results) != 1 || results[0].Exit == 0 || !strings.Contains(results[0].Detail, "outside") {
		t.Fatalf("%+v\n%s", results, out)
	}
	assertNpmSilent(t, logPath)

	// A symlink inside the cache that points outside is also refused.
	if err := os.WriteFile(filepath.Join(stampDir, "codex"), []byte("npm\t@openai/codex\t"+filepath.Join(linkDir, "openai-codex-1.0.0.tgz")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(linkDir, "openai-codex-1.0.0.tgz")); err != nil {
		t.Fatal(err)
	}
	out = runSh(t, script, env)
	results, _, err = ParseApply(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(results) != 1 || !strings.Contains(results[0].Detail, "symlink") {
		t.Fatalf("%+v\n%s", results, out)
	}
	assertNpmSilent(t, logPath)
}

func TestFetchApplyBrewRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	bin := filepath.Join(root, "bin")
	logPath := filepath.Join(root, "brew.log")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExe(t, filepath.Join(bin, "brew"), `#!/bin/sh
printf '%s HOMEBREW_NO_AUTO_UPDATE=%s\n' "$*" "${HOMEBREW_NO_AUTO_UPDATE:-}" >> "$BREW_LOG"
exit 0
`)
	fetch, err := FetchScript([]string{bin}, []StageStep{
		{Tool: "claude", Kind: catalog.MethodBrew, BrewKind: "cask", BrewToken: "claude-code"},
		{Tool: "opencode", Kind: catalog.MethodBrew, BrewKind: "formula", BrewToken: "opencode"},
	})
	if err != nil {
		t.Fatal(err)
	}
	env := []string{
		"PATH=" + bin + ":/usr/bin:/bin",
		"HOME=" + home,
		"BREW_LOG=" + logPath,
		"HOMEBREW_NO_AUTO_UPDATE=1",
	}
	out := runSh(t, fetch, env)
	results, err := ParseFetch(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(results) != 2 || results[0].Exit != 0 || results[1].Exit != 0 {
		t.Fatalf("%+v\n%s", results, out)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(log)
	if strings.Count(text, "update ") != 1 {
		t.Fatalf("brew update count:\n%s", text)
	}
	if !strings.Contains(text, "fetch --cask claude-code") || !strings.Contains(text, "fetch --formula opencode") {
		t.Fatalf("fetch args:\n%s", text)
	}
	if strings.Contains(text, "upgrade") || strings.Contains(text, "install") {
		t.Fatalf("fetch upgraded:\n%s", text)
	}
	// The explicit update runs with auto-update unset, then fetch freezes it.
	if !strings.Contains(text, "update HOMEBREW_NO_AUTO_UPDATE=\n") && !strings.HasPrefix(text, "update HOMEBREW_NO_AUTO_UPDATE=\n") {
		t.Fatalf("brew update should ignore a pre-set HOMEBREW_NO_AUTO_UPDATE:\n%s", text)
	}
	if !strings.Contains(text, "fetch --cask claude-code HOMEBREW_NO_AUTO_UPDATE=1") {
		t.Fatalf("brew fetch should not update again:\n%s", text)
	}

	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	apply, err := ApplyScript([]Tool{{Name: "claude", Binary: "claude"}, {Name: "opencode", Binary: "opencode"}}, []string{bin}, []StageStep{
		{Tool: "claude", Kind: catalog.MethodBrew, BrewKind: "cask", BrewToken: "claude-code"},
		{Tool: "opencode", Kind: catalog.MethodBrew, BrewKind: "formula", BrewToken: "opencode"},
	})
	if err != nil {
		t.Fatal(err)
	}
	writeExe(t, filepath.Join(bin, "claude"), "#!/bin/sh\nprintf '2.0.0\\n'\n")
	writeExe(t, filepath.Join(bin, "opencode"), "#!/bin/sh\nprintf '1.0.0\\n'\n")
	out = runSh(t, apply, env)
	applied, _, err := ParseApply(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(applied) != 2 || applied[0].Exit != 0 || applied[1].Exit != 0 {
		t.Fatalf("%+v\n%s", applied, out)
	}
	log, err = os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	text = string(log)
	if !strings.Contains(text, "upgrade --cask claude-code HOMEBREW_NO_AUTO_UPDATE=1") {
		t.Fatalf("cask apply:\n%s", text)
	}
	if !strings.Contains(text, "upgrade opencode HOMEBREW_NO_AUTO_UPDATE=1") {
		t.Fatalf("formula apply:\n%s", text)
	}
	if strings.Contains(text, "update ") || strings.Contains(text, "fetch ") {
		t.Fatalf("apply downloaded again:\n%s", text)
	}
}

func runSh(t *testing.T, script string, env []string) string {
	t.Helper()
	cmd := exec.Command("sh", "-s")
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	return string(out)
}

func assertNpmSilent(t *testing.T, logPath string) {
	t.Helper()
	body, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatal(err)
	}
	if len(bytesTrim(body)) != 0 {
		t.Fatalf("npm should not have run:\n%s", body)
	}
	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func bytesTrim(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}
