package probe

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

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
	// The one npm install fetch may run fills npm's cache through a
	// throwaway prefix; any other npm install would touch a live package.
	warm := `npm install -g --prefix "$cf_nw_prefix"`
	if strings.Count(fetch, warm) != 1 {
		t.Fatalf("fetch script should warm the npm cache once, got %d", strings.Count(fetch, warm))
	}
	for _, bad := range []string{"npm install", "brew upgrade", "brew install", "'claude' 'update'", "codex update", "@latest@latest"} {
		if strings.Contains(strings.Replace(fetch, warm, "", 1), bad) {
			t.Fatalf("fetch script contains %q", bad)
		}
	}
	if strings.Count(fetch, `brew update >"$cf_dir/brew-update.log"`) != 2 {
		t.Fatalf("brew update should be the shared preamble plus the stage_brew fallback, got %d", strings.Count(fetch, `brew update >"$cf_dir/brew-update.log"`))
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
		"npm install -g --prefix",
		"--no-fund --no-audit",
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
if [ "$1" = root ]; then
  printf '%s\n' "$LIVE_ROOT"
  exit 0
fi
if [ "$1" = install ]; then
  prefix=.
  prev=
  for a in "$@"; do
    if [ "$prev" = --prefix ]; then
      prefix=$a
    fi
    prev=$a
  done
  pkgdir="$prefix/lib/node_modules/@openai/codex/bin"
  mkdir -p "$pkgdir"
  printf '%s\n' '{"name":"@openai/codex","version":"9.9.9","bin":{"codex":"bin/codex.js"}}' > "$prefix/lib/node_modules/@openai/codex/package.json"
  printf '%s\n' '#!/bin/sh' 'printf "codex-cli 9.9.9\n"' > "$pkgdir/codex.js"
  chmod 755 "$pkgdir/codex.js"
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
		"LIVE_ROOT=" + filepath.Join(root, "live", "lib", "node_modules"),
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
	if !strings.Contains(string(log), "--prefix") || !strings.Contains(string(log), "--no-fund --no-audit "+staged) {
		t.Fatalf("npm install args:\n%s", log)
	}
	if strings.Contains(string(log), "install -g --no-fund --no-audit "+staged) && !strings.Contains(string(log), "--prefix") {
		t.Fatalf("npm install rewrote the live prefix:\n%s", log)
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

// runSh runs a generated script with a hard deadline. If a fake binary is
// shadowed by a real one (a network npm pack, say), the test fails in a
// minute instead of holding the package until go test's 10m timeout.
func runSh(t *testing.T, script string, env []string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-s")
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("script did not finish in time; a real binary may be shadowing a fake one:\n%s", out)
	}
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

func TestEngineRangeMatchesOpenClaw(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	script := rangeShell + "cf_range_result \"$1\" \"$2\"\n"
	cases := []struct{ have, rng, want string }{
		{"v22.23.4", ">=24.22.1 <25 || >=26.12.1", "no"},
		{"v24.22.0", ">=24.22.1 <25 || >=26.12.1", "no"},
		{"v24.22.1", ">=24.22.1 <25 || >=26.12.1", "yes"},
		{"v25.0.0", ">=24.22.1 <25 || >=26.12.1", "no"},
		{"v26.12.0", ">=24.22.1 <25 || >=26.12.1", "no"},
		{"v26.12.1", ">=24.22.1 <25 || >=26.12.1", "yes"},
		{"1.2.3", ">=1.2.3", "yes"},
		{"1.2.2", ">=1.2.3", "no"},
	}
	for _, c := range cases {
		cmd := exec.Command("sh", "-s", c.have, c.rng)
		cmd.Stdin = strings.NewReader(script)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %s: %v\n%s", c.have, c.rng, err, out)
		}
		if strings.TrimSpace(string(out)) != c.want {
			t.Fatalf("%s vs %s: got %q want %s", c.have, c.rng, out, c.want)
		}
	}
}

func TestShellScriptsParse(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	fetch, err := FetchScript(nil, []StageStep{
		{Tool: "codex", Kind: catalog.MethodNpm, Package: "@openai/codex"},
		{Tool: "claude", Kind: catalog.KindRelease, Release: "claude"},
	})
	if err != nil {
		t.Fatal(err)
	}
	apply, err := ApplyScript([]Tool{{Name: "codex", Binary: "codex"}}, nil, []StageStep{
		{Tool: "codex", Kind: catalog.MethodNpm, Package: "@openai/codex"},
		{Tool: "claude", Kind: catalog.KindRelease, Release: "claude"},
	})
	if err != nil {
		t.Fatal(err)
	}
	probe, err := ProbeScript([]Tool{{Name: "codex", Binary: "codex"}}, []string{"/opt/custom/bin"})
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"fetch": fetch, "apply": apply, "probe": probe} {
		cmd := exec.Command("sh", "-n")
		cmd.Stdin = strings.NewReader(body)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", name, err, out)
		}
	}
}

func TestNpmApplySkipsCurrentAndRestoresOnFailure(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	bin := filepath.Join(root, "bin")
	prefix := filepath.Join(root, "prefix")
	pkgDir := filepath.Join(prefix, "lib", "node_modules", "@openai", "codex")
	if err := os.MkdirAll(filepath.Join(pkgDir, "bin"), 0o755); err != nil || os.MkdirAll(bin, 0o755) != nil || os.MkdirAll(filepath.Join(prefix, "bin"), 0o755) != nil {
		t.Fatal(err)
	}
	writeExe(t, filepath.Join(pkgDir, "bin", "codex.js"), "#!/bin/sh\nprintf 'codex-cli 1.2.3\\n'\n")
	if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte("{\"name\":\"@openai/codex\",\"version\":\"1.2.3\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../lib/node_modules/@openai/codex/bin/codex.js", filepath.Join(prefix, "bin", "codex")); err != nil {
		t.Fatal(err)
	}
	tarball := filepath.Join(home, ".cache", "clifleet", "npm", "codex", "openai-codex-1.2.3.tgz")
	if err := os.MkdirAll(filepath.Dir(tarball), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTarGz(t, tarball, map[string]string{
		"package/package.json": "{\"name\":\"@openai/codex\",\"version\":\"1.2.3\"}\n",
		"package/bin/codex.js": "#!/bin/sh\nprintf 'codex-cli 1.2.3\\n'\n",
	})
	if err := os.MkdirAll(filepath.Join(home, ".cache", "clifleet", "staged"), 0o755); err != nil {
		t.Fatal(err)
	}
	stamp := "npm\t@openai/codex\t" + tarball + "\t1.2.3\t-\n"
	if err := os.WriteFile(filepath.Join(home, ".cache", "clifleet", "staged", "codex"), []byte(stamp), 0o644); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "npm.log")
	writeExe(t, filepath.Join(bin, "npm"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$NPM_LOG\"\nexit 0\n")
	script, err := ApplyScript([]Tool{{Name: "codex", Binary: "codex"}}, []string{filepath.Join(prefix, "bin"), bin}, []StageStep{{
		Tool: "codex", Kind: catalog.MethodNpm, Package: "@openai/codex",
	}})
	if err != nil {
		t.Fatal(err)
	}
	env := []string{
		"PATH=" + filepath.Join(prefix, "bin") + ":" + bin + ":/usr/bin:/bin",
		"HOME=" + home,
		"NPM_LOG=" + logPath,
		"LIVE_ROOT=" + filepath.Join(prefix, "lib", "node_modules"),
	}
	out := runSh(t, script, env)
	results, _, err := ParseApply(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(results) != 1 || results[0].Skip == "" || !strings.Contains(results[0].Skip, "already 1.2.3") {
		t.Fatalf("current apply %+v\n%s", results, out)
	}
	assertNpmSilent(t, logPath)

	// A newer tarball whose install exits must not replace a working binary.
	if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte("{\"name\":\"@openai/codex\",\"version\":\"1.0.0\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeExe(t, filepath.Join(pkgDir, "bin", "codex.js"), "#!/bin/sh\nprintf 'codex-cli 1.0.0\\n'\n")
	bad := filepath.Join(home, ".cache", "clifleet", "npm", "codex", "openai-codex-2.0.0.tgz")
	writeTarGz(t, bad, map[string]string{
		"package/package.json": "{\"name\":\"@openai/codex\",\"version\":\"2.0.0\"}\n",
	})
	if err := os.WriteFile(filepath.Join(home, ".cache", "clifleet", "staged", "codex"), []byte("npm\t@openai/codex\t"+bad+"\t2.0.0\t-\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeExe(t, filepath.Join(bin, "npm"), `#!/bin/sh
printf '%s\n' "$*" >> "$NPM_LOG"
if [ "$1" = root ]; then
  printf '%s\n' "$LIVE_ROOT"
  exit 0
fi
if [ "$1" = install ]; then
  prefix=.
  prev=
  for a in "$@"; do
    if [ "$prev" = --prefix ]; then prefix=$a; fi
    prev=$a
  done
  mkdir -p "$prefix/lib/node_modules/@openai/codex/bin"
  printf '%s\n' '{"name":"@openai/codex","version":"2.0.0"}' > "$prefix/lib/node_modules/@openai/codex/package.json"
  printf '%s\n' '#!/bin/sh' 'exit 1' > "$prefix/lib/node_modules/@openai/codex/bin/codex.js"
  chmod 755 "$prefix/lib/node_modules/@openai/codex/bin/codex.js"
  exit 0
fi
exit 0
`)
	out = runSh(t, script, env)
	results, _, err = ParseApply(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(results) != 1 || results[0].Exit == 0 || !strings.Contains(results[0].Detail, "restored") {
		t.Fatalf("failed apply %+v\n%s", results, out)
	}
	still, err := exec.Command(filepath.Join(prefix, "bin", "codex"), "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(still), "1.0.0") {
		t.Fatalf("previous codex was not restored: %q", still)
	}
}

func TestNpmFetchBlocksEngineMismatch(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	bin := filepath.Join(root, "bin")
	cache := filepath.Join(root, "xdg")
	if err := os.MkdirAll(bin, 0o755); err != nil || os.MkdirAll(home, 0o755) != nil {
		t.Fatal(err)
	}
	src := filepath.Join(root, "openclaw.tgz")
	writeTarGz(t, src, map[string]string{
		"package/package.json": "{\"name\":\"openclaw\",\"version\":\"2026.10.7\",\"engines\":{\"node\":\">=24.22.1 <25 || >=26.12.1\"}}\n",
	})
	writeExe(t, filepath.Join(bin, "node"), "#!/bin/sh\nif [ \"$1\" = --version ]; then printf 'v22.23.4\\n'; exit 0; fi\nexit 1\n")
	writeExe(t, filepath.Join(bin, "npm"), `#!/bin/sh
if [ "$1" = pack ]; then
  dest=.
  prev=
  for a in "$@"; do
    if [ "$prev" = --pack-destination ]; then dest=$a; fi
    prev=$a
  done
  mkdir -p "$dest"
  cp "$SRC_TGZ" "$dest/openclaw-2026.10.7.tgz"
  printf '%s\n' "openclaw-2026.10.7.tgz"
  exit 0
fi
exit 0
`)
	script, err := FetchScript([]string{bin}, []StageStep{{Tool: "openclaw", Kind: catalog.MethodNpm, Package: "openclaw"}})
	if err != nil {
		t.Fatal(err)
	}
	out := runSh(t, script, []string{
		"PATH=" + bin + ":/usr/bin:/bin",
		"HOME=" + home,
		"XDG_CACHE_HOME=" + cache,
		"SRC_TGZ=" + src,
	})
	results, err := ParseFetch(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(results) != 1 || results[0].Blocked == "" || results[0].Exit != 0 {
		t.Fatalf("%+v\n%s", results, out)
	}
	if !strings.Contains(results[0].Blocked, "24.22.1") || !strings.Contains(results[0].Blocked, "v22.23.4") {
		t.Fatalf("blocked detail %q", results[0].Blocked)
	}
}

func TestCodexReleaseFetchThenSwap(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	bin := filepath.Join(root, "bin")
	cache := filepath.Join(root, "xdg")
	if err := os.MkdirAll(bin, 0o755); err != nil || os.MkdirAll(home, 0o755) != nil {
		t.Fatal(err)
	}
	body := filepath.Join(root, "codex-package.tgz")
	writeTarGz(t, body, map[string]string{
		"bin/codex":          "#!/bin/sh\nprintf 'codex-cli 1.2.3\\n'\n",
		"codex-package.json": "{\"version\":\"1.2.3\",\"target\":\"x86_64-unknown-linux-musl\",\"entrypoint\":\"bin/codex\"}\n",
	})
	writeExe(t, filepath.Join(bin, "curl"), `#!/bin/sh
out=
url=
prev=
for a in "$@"; do
  printf '%s\n' "$a" >> "$CURL_LOG"
  if [ "$prev" = -o ]; then out=$a; fi
  case $a in http*) url=$a ;; esac
  prev=$a
done
case $url in
  */channels/latest) printf '%s\n' '{"tag_name":"rust-v1.2.3"}' > "$out" ;;
  *) cp "$CURL_BODY" "$out" ;;
esac
`)
	standalone := filepath.Join(home, ".codex", "packages", "standalone")
	oldRel := filepath.Join(standalone, "releases", "1.0.0-old")
	if err := os.MkdirAll(filepath.Join(oldRel, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeExe(t, filepath.Join(oldRel, "bin", "codex"), "#!/bin/sh\nprintf 'codex-cli 1.0.0\\n'\n")
	if err := os.Symlink(oldRel, filepath.Join(standalone, "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(standalone, "current", "bin", "codex"), filepath.Join(bin, "codex")); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "curl.log")
	step := StageStep{Tool: "codex", Kind: catalog.KindRelease, Release: "codex"}
	fetch, err := FetchScript([]string{bin}, []StageStep{step})
	if err != nil {
		t.Fatal(err)
	}
	env := []string{
		"PATH=" + bin + ":/usr/bin:/bin",
		"HOME=" + home,
		"XDG_CACHE_HOME=" + cache,
		"CURL_LOG=" + logPath,
		"CURL_BODY=" + body,
	}
	out := runSh(t, fetch, env)
	results, err := ParseFetch(out)
	if err != nil {
		t.Fatalf("fetch: %v\n%s", err, out)
	}
	if len(results) != 1 || results[0].Exit != 0 || results[0].Blocked != "" {
		t.Fatalf("fetch %+v\n%s", results, out)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logged), "https://releases.openai.com/codex/releases/1.2.3/codex-package-") || !strings.Contains(string(logged), ".tar.gz") {
		t.Fatalf("curl url:\n%s", logged)
	}
	apply, err := ApplyScript([]Tool{{Name: "codex", Binary: "codex"}}, []string{bin}, []StageStep{step})
	if err != nil {
		t.Fatal(err)
	}
	out = runSh(t, apply, env)
	applied, obs, err := ParseApply(out)
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if len(applied) != 1 || applied[0].Exit != 0 {
		t.Fatalf("apply %+v\n%s", applied, out)
	}
	if ShortVersion(obs["codex"].Version) != "1.2.3" {
		t.Fatalf("reprobe %+v", obs["codex"])
	}
	live, err := os.ReadFile(filepath.Join(bin, "codex"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "codex"), append(live, []byte("# keep\n")...), 0o755); err != nil {
		t.Fatal(err)
	}
	out = runSh(t, apply, env)
	applied, _, err = ParseApply(out)
	if err != nil {
		t.Fatalf("second apply: %v\n%s", err, out)
	}
	if len(applied) != 1 || !applied[0].Current || !strings.Contains(applied[0].Detail, "up to date") {
		t.Fatalf("second apply %+v\n%s", applied, out)
	}
	live, err = os.ReadFile(filepath.Join(bin, "codex"))
	if err != nil || !strings.Contains(string(live), "# keep") {
		t.Fatalf("up-to-date apply rewrote codex: %v %q", err, live)
	}
}

func TestCodexStandaloneLinkFlipLeavesOldRelease(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	bin := filepath.Join(root, "bin")
	oldRel := filepath.Join(home, ".codex", "packages", "standalone", "releases", "1.0.0-old")
	localBin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(filepath.Join(oldRel, "bin"), 0o755); err != nil || os.MkdirAll(bin, 0o755) != nil || os.MkdirAll(localBin, 0o755) != nil {
		t.Fatal(err)
	}
	writeExe(t, filepath.Join(oldRel, "bin", "codex"), "#!/bin/sh\nprintf 'codex-cli 1.0.0\\n'\n")
	standalone := filepath.Join(home, ".codex", "packages", "standalone")
	if err := os.Symlink(oldRel, filepath.Join(standalone, "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(standalone, "current", "bin", "codex"), filepath.Join(localBin, "codex")); err != nil {
		t.Fatal(err)
	}
	body := filepath.Join(root, "codex-package.tgz")
	writeTarGz(t, body, map[string]string{
		"bin/codex":          "#!/bin/sh\nprintf 'codex-cli 1.2.3\\n'\n",
		"codex-package.json": "{\"version\":\"1.2.3\",\"entrypoint\":\"bin/codex\"}\n",
	})
	writeExe(t, filepath.Join(bin, "curl"), `#!/bin/sh
out=
url=
prev=
for a in "$@"; do
  if [ "$prev" = -o ]; then out=$a; fi
  case $a in http*) url=$a ;; esac
  prev=$a
done
case $url in
  */channels/latest) printf '%s\n' '{"tag_name":"rust-v1.2.3"}' > "$out" ;;
  *) cp "$CURL_BODY" "$out" ;;
esac
`)
	step := StageStep{Tool: "codex", Kind: catalog.KindRelease, Release: "codex"}
	fetch, err := FetchScript([]string{bin, localBin}, []StageStep{step})
	if err != nil {
		t.Fatal(err)
	}
	env := []string{
		"PATH=" + localBin + ":" + bin + ":/usr/bin:/bin",
		"HOME=" + home,
		"CURL_BODY=" + body,
	}
	out := runSh(t, fetch, env)
	if results, err := ParseFetch(out); err != nil || len(results) != 1 || results[0].Exit != 0 {
		t.Fatalf("fetch %v %+v\n%s", err, results, out)
	}
	apply, err := ApplyScript([]Tool{{Name: "codex", Binary: "codex"}}, []string{bin, localBin}, []StageStep{step})
	if err != nil {
		t.Fatal(err)
	}
	out = runSh(t, apply, env)
	applied, obs, err := ParseApply(out)
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if len(applied) != 1 || applied[0].Exit != 0 {
		t.Fatalf("apply %+v\n%s", applied, out)
	}
	if ShortVersion(obs["codex"].Version) != "1.2.3" {
		t.Fatalf("apply %+v reprobe %+v\n%s", applied, obs["codex"], out)
	}
	current, err := os.Readlink(filepath.Join(standalone, "current"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(current, "1.2.3-") {
		t.Fatalf("current link %s", current)
	}
	if _, err := os.Stat(filepath.Join(oldRel, "bin", "codex")); err != nil {
		t.Fatalf("old release was removed: %v", err)
	}
}

func TestReleaseFetchBlocksUnknownPlatform(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExe(t, filepath.Join(bin, "uname"), "#!/bin/sh\ncase $1 in -s) printf 'Haiku\\n';; -m) printf 'm68k\\n';; esac\n")
	writeExe(t, filepath.Join(bin, "curl"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$CURL_LOG\"\nexit 1\n")
	script, err := FetchScript([]string{bin}, []StageStep{{Tool: "codex", Kind: catalog.KindRelease, Release: "codex"}})
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "curl.log")
	out := runSh(t, script, []string{
		"PATH=" + bin + ":/usr/bin:/bin",
		"HOME=" + filepath.Join(root, "home"),
		"CURL_LOG=" + logPath,
	})
	results, err := ParseFetch(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(results) != 1 || results[0].Blocked == "" || !strings.Contains(results[0].Blocked, "Haiku") {
		t.Fatalf("%+v\n%s", results, out)
	}
	if _, err := os.Stat(logPath); err == nil {
		body, _ := os.ReadFile(logPath)
		if len(bytesTrim(body)) != 0 {
			t.Fatalf("curl ran for an unknown platform:\n%s", body)
		}
	}
}

func TestClaudeManifestSwapRetargetsSymlink(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil || exec.Command("sha256sum", "--version").Run() != nil && exec.Command("shasum", "-a", "256", "/dev/null").Run() != nil {
		t.Skip("sh or sha256 tool not available")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	bin := filepath.Join(root, "bin")
	versions := filepath.Join(home, ".local", "share", "claude", "versions")
	localBin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(versions, 0o755); err != nil || os.MkdirAll(localBin, 0o755) != nil || os.MkdirAll(bin, 0o755) != nil {
		t.Fatal(err)
	}
	old := filepath.Join(versions, "2.1.0")
	writeExe(t, old, "#!/bin/sh\nprintf '2.1.0 (Claude Code)\\n'\n")
	if err := os.Symlink(old, filepath.Join(localBin, "claude")); err != nil {
		t.Fatal(err)
	}
	newBin := filepath.Join(root, "claude-new")
	writeExe(t, newBin, "#!/bin/sh\nprintf '2.1.40 (Claude Code)\\n'\n")
	sumOut, err := exec.Command("sha256sum", newBin).Output()
	if err != nil {
		sumOut, err = exec.Command("shasum", "-a", "256", newBin).Output()
		if err != nil {
			t.Fatal(err)
		}
	}
	sum := strings.Fields(string(sumOut))[0]
	manifest := filepath.Join(root, "manifest.json")
	body := "{\n  \"version\": \"2.1.40\",\n  \"platforms\": {\n"
	for _, plat := range []string{"darwin-arm64", "darwin-x64", "linux-arm64", "linux-x64", "linux-arm64-musl", "linux-x64-musl"} {
		body += "    \"" + plat + "\": {\"binary\": \"claude\", \"checksum\": \"" + sum + "\"},\n"
	}
	body += "    \"unused\": {\"binary\": \"claude\", \"checksum\": \"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\"}\n  }\n}\n"
	if err := os.WriteFile(manifest, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	writeExe(t, filepath.Join(bin, "curl"), `#!/bin/sh
out=
url=
prev=
for a in "$@"; do
  if [ "$prev" = -o ]; then out=$a; fi
  case $a in http*) url=$a ;; esac
  prev=$a
done
case $url in
  */latest) printf '%s\n' "$CLAUDE_VER" > "$out" ;;
  */manifest.json.sig) printf 'sig\n' > "$out" ;;
  */manifest.json) cp "$CLAUDE_MANIFEST" "$out" ;;
  */claude) cp "$CLAUDE_BIN" "$out" ;;
  *) printf 'unexpected %s\n' "$url" >&2; exit 1 ;;
esac
`)
	step := StageStep{Tool: "claude", Kind: catalog.KindRelease, Release: "claude"}
	fetch, err := FetchScript([]string{bin, localBin}, []StageStep{step})
	if err != nil {
		t.Fatal(err)
	}
	env := []string{
		"PATH=" + localBin + ":" + bin + ":/usr/bin:/bin",
		"HOME=" + home,
		"CLAUDE_VER=2.1.40",
		"CLAUDE_MANIFEST=" + manifest,
		"CLAUDE_BIN=" + newBin,
	}
	out := runSh(t, fetch, env)
	results, err := ParseFetch(out)
	if err != nil || len(results) != 1 || results[0].Exit != 0 || results[0].Blocked != "" {
		t.Fatalf("fetch %v %+v\n%s", err, results, out)
	}
	apply, err := ApplyScript([]Tool{{Name: "claude", Binary: "claude"}}, []string{bin, localBin}, []StageStep{step})
	if err != nil {
		t.Fatal(err)
	}
	out = runSh(t, apply, env)
	applied, obs, err := ParseApply(out)
	if err != nil {
		t.Fatalf("apply %v\n%s", err, out)
	}
	if len(applied) != 1 || applied[0].Exit != 0 {
		t.Fatalf("apply %+v\n%s", applied, out)
	}
	if ShortVersion(obs["claude"].Version) != "2.1.40" {
		t.Fatalf("reprobe %+v", obs["claude"])
	}
	link, err := os.Readlink(filepath.Join(localBin, "claude"))
	if err != nil || !strings.HasSuffix(link, "/versions/2.1.40") {
		t.Fatalf("symlink %s %v", link, err)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("old claude version removed: %v", err)
	}
	out = runSh(t, apply, env)
	applied, _, err = ParseApply(out)
	if err != nil || len(applied) != 1 || !applied[0].Current {
		t.Fatalf("second apply %+v %v\n%s", applied, err, out)
	}

	// A manifest checksum that does not match the bytes must not retarget the symlink.
	bad := filepath.Join(root, "bad-manifest.json")
	if err := os.WriteFile(bad, []byte("{\n  \"platforms\": {\n    \"linux-x64\": {\"checksum\": \"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\"},\n    \"darwin-arm64\": {\"checksum\": \"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\"}\n  }\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env[3] = "CLAUDE_MANIFEST=" + bad
	env[2] = "CLAUDE_VER=2.1.41"
	out = runSh(t, fetch, env)
	results, err = ParseFetch(out)
	if err != nil || len(results) != 1 || results[0].Exit == 0 || !strings.Contains(results[0].Detail, "checksum") {
		t.Fatalf("bad checksum %+v %v\n%s", results, err, out)
	}
	link, _ = os.Readlink(filepath.Join(localBin, "claude"))
	if !strings.HasSuffix(link, "/versions/2.1.40") {
		t.Fatalf("checksum failure moved the symlink to %s", link)
	}
}

func TestCursorAgentVersionDirSwap(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	bin := filepath.Join(root, "bin")
	localBin := filepath.Join(home, ".local", "bin")
	oldDir := filepath.Join(home, ".local", "share", "cursor-agent", "versions", "2026.01.01-aaaa")
	if err := os.MkdirAll(filepath.Join(oldDir), 0o755); err != nil || os.MkdirAll(localBin, 0o755) != nil || os.MkdirAll(bin, 0o755) != nil {
		t.Fatal(err)
	}
	writeExe(t, filepath.Join(oldDir, "cursor-agent"), "#!/bin/sh\nprintf '2026.01.01-aaaa\\n'\n")
	if err := os.Symlink(filepath.Join(oldDir, "cursor-agent"), filepath.Join(localBin, "agent")); err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(root, "agent.tgz")
	writeTarGz(t, pkg, map[string]string{
		"payload/cursor-agent": "#!/bin/sh\nprintf '2026.10.28-abc123\\n'\n",
	})
	install := filepath.Join(root, "install.sh")
	if err := os.WriteFile(install, []byte("DOWNLOAD_URL=\"https://downloads.cursor.com/lab/2026.10.28-abc123/${OS}/${ARCH}/agent-cli-package.tar.gz\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeExe(t, filepath.Join(bin, "curl"), `#!/bin/sh
out=
url=
prev=
for a in "$@"; do
  if [ "$prev" = -o ]; then out=$a; fi
  case $a in http*) url=$a ;; esac
  prev=$a
done
case $url in
  https://cursor.com/install) cp "$INSTALL_SH" "$out" ;;
  https://downloads.cursor.com/lab/*/agent-cli-package.tar.gz) cp "$AGENT_TGZ" "$out" ;;
  *) printf 'unexpected %s\n' "$url" >&2; exit 1 ;;
esac
`)
	step := StageStep{Tool: "agent", Kind: catalog.KindRelease, Release: "agent"}
	fetch, err := FetchScript([]string{bin, localBin}, []StageStep{step})
	if err != nil {
		t.Fatal(err)
	}
	env := []string{
		"PATH=" + localBin + ":" + bin + ":/usr/bin:/bin",
		"HOME=" + home,
		"INSTALL_SH=" + install,
		"AGENT_TGZ=" + pkg,
	}
	out := runSh(t, fetch, env)
	results, err := ParseFetch(out)
	if err != nil || len(results) != 1 || results[0].Exit != 0 {
		t.Fatalf("fetch %v %+v\n%s", err, results, out)
	}
	apply, err := ApplyScript([]Tool{{Name: "agent", Binary: "agent"}}, []string{bin, localBin}, []StageStep{step})
	if err != nil {
		t.Fatal(err)
	}
	out = runSh(t, apply, env)
	applied, obs, err := ParseApply(out)
	if err != nil {
		t.Fatalf("apply %v\n%s", err, out)
	}
	if len(applied) != 1 || applied[0].Exit != 0 {
		t.Fatalf("apply %+v\n%s", applied, out)
	}
	if !strings.Contains(obs["agent"].Version, "2026.10.28-abc123") {
		t.Fatalf("reprobe %+v", obs["agent"])
	}
	link, err := os.Readlink(filepath.Join(localBin, "agent"))
	if err != nil || !strings.Contains(link, "/versions/2026.10.28-abc123/cursor-agent") {
		t.Fatalf("symlink %s %v", link, err)
	}
	if _, err := os.Stat(filepath.Join(oldDir, "cursor-agent")); err != nil {
		t.Fatalf("old cursor-agent version removed: %v", err)
	}
}

func TestNpmSwapRemovesCodexJunkDirs(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	bin := filepath.Join(root, "bin")
	prefix := filepath.Join(root, "prefix")
	scope := filepath.Join(prefix, "lib", "node_modules", "@openai")
	pkgDir := filepath.Join(scope, "codex")
	junk := filepath.Join(scope, ".codex-partial")
	if err := os.MkdirAll(filepath.Join(pkgDir, "bin"), 0o755); err != nil || os.MkdirAll(junk, 0o755) != nil || os.MkdirAll(bin, 0o755) != nil || os.MkdirAll(filepath.Join(prefix, "bin"), 0o755) != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(junk, "leftover"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeExe(t, filepath.Join(pkgDir, "bin", "codex.js"), "#!/bin/sh\nprintf 'codex-cli 1.0.0\\n'\n")
	if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte("{\"name\":\"@openai/codex\",\"version\":\"1.0.0\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../lib/node_modules/@openai/codex/bin/codex.js", filepath.Join(prefix, "bin", "codex")); err != nil {
		t.Fatal(err)
	}
	tarball := filepath.Join(home, ".cache", "clifleet", "npm", "codex", "openai-codex-2.0.0.tgz")
	writeTarGz(t, tarball, map[string]string{
		"package/package.json": "{\"name\":\"@openai/codex\",\"version\":\"2.0.0\"}\n",
		"package/bin/codex.js": "#!/bin/sh\nprintf 'codex-cli 2.0.0\\n'\n",
	})
	if err := os.MkdirAll(filepath.Join(home, ".cache", "clifleet", "staged"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".cache", "clifleet", "staged", "codex"), []byte("npm\t@openai/codex\t"+tarball+"\t2.0.0\t-\t-\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "npm.log")
	writeExe(t, filepath.Join(bin, "npm"), `#!/bin/sh
printf '%s\n' "$*" >> "$NPM_LOG"
if [ "$1" = root ]; then printf '%s\n' "$LIVE_ROOT"; exit 0; fi
if [ "$1" = install ]; then
  prefix=.
  prev=
  for a in "$@"; do
    if [ "$prev" = --prefix ]; then prefix=$a; fi
    prev=$a
  done
  mkdir -p "$prefix/lib/node_modules/@openai/codex/bin"
  printf '%s\n' '{"name":"@openai/codex","version":"2.0.0","bin":{"codex":"bin/codex.js"}}' > "$prefix/lib/node_modules/@openai/codex/package.json"
  printf '%s\n' '#!/bin/sh' 'printf "codex-cli 2.0.0\n"' > "$prefix/lib/node_modules/@openai/codex/bin/codex.js"
  chmod 755 "$prefix/lib/node_modules/@openai/codex/bin/codex.js"
  exit 0
fi
exit 0
`)
	script, err := ApplyScript([]Tool{{Name: "codex", Binary: "codex"}}, []string{filepath.Join(prefix, "bin"), bin}, []StageStep{{
		Tool: "codex", Kind: catalog.MethodNpm, Package: "@openai/codex",
	}})
	if err != nil {
		t.Fatal(err)
	}
	out := runSh(t, script, []string{
		"PATH=" + filepath.Join(prefix, "bin") + ":" + bin + ":/usr/bin:/bin",
		"HOME=" + home,
		"NPM_LOG=" + logPath,
		"LIVE_ROOT=" + filepath.Join(prefix, "lib", "node_modules"),
	})
	results, _, err := ParseApply(out)
	if err != nil || len(results) != 1 || results[0].Exit != 0 {
		t.Fatalf("%v %+v\n%s", err, results, out)
	}
	if _, err := os.Stat(junk); !os.IsNotExist(err) {
		t.Fatalf("junk dir still present: %v", err)
	}
	still, err := exec.Command(filepath.Join(prefix, "bin", "codex"), "--version").Output()
	if err != nil || !strings.Contains(string(still), "2.0.0") {
		t.Fatalf("codex after swap %v %q", err, still)
	}
	log, err := os.ReadFile(logPath)
	if err != nil || !strings.Contains(string(log), "--include=optional") {
		t.Fatalf("install log:\n%s", log)
	}
}

func TestCodexApplyRefusesMiseShim(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	bin := filepath.Join(root, "bin")
	shimDir := filepath.Join(home, ".local", "share", "mise", "shims")
	if err := os.MkdirAll(bin, 0o755); err != nil || os.MkdirAll(shimDir, 0o755) != nil {
		t.Fatal(err)
	}
	mise := filepath.Join(bin, "mise")
	if err := os.WriteFile(mise, []byte("MISE-BINARY\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(mise, filepath.Join(shimDir, "codex")); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "codex-package.tgz")
	writeTarGz(t, archive, map[string]string{
		"bin/codex":          "#!/bin/sh\nprintf 'codex-cli 1.2.3\\n'\n",
		"codex-package.json": "{\"version\":\"1.2.3\"}\n",
	})
	cache := filepath.Join(home, ".cache", "clifleet")
	if err := os.MkdirAll(filepath.Join(cache, "staged"), 0o755); err != nil {
		t.Fatal(err)
	}
	stamp := "release\tcodex\tcodex-package.tgz\t" + archive + "\n"
	if err := os.WriteFile(filepath.Join(cache, "staged", "codex"), []byte(stamp), 0o644); err != nil {
		t.Fatal(err)
	}
	step := StageStep{Tool: "codex", Kind: catalog.KindRelease, Release: "codex"}
	apply, err := ApplyScript([]Tool{{Name: "codex", Binary: "codex"}}, []string{bin}, []StageStep{step})
	if err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + home}
	out := runSh(t, apply, env)
	results, _, err := ParseApply(out)
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if len(results) != 1 || results[0].Blocked == "" || !strings.Contains(results[0].Blocked, "managed by mise") {
		t.Fatalf("%+v\n%s", results, out)
	}
	body, err := os.ReadFile(mise)
	if err != nil || string(body) != "MISE-BINARY\n" {
		t.Fatalf("mise binary changed: %v %q", err, body)
	}

	// A plain codex ahead of the shim is not a standalone install either: it
	// could be npm's codex.js or a wrapper, and apply must leave it alone.
	localBin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(localBin, 0o755); err != nil {
		t.Fatal(err)
	}
	const plain = "#!/bin/sh\nprintf 'codex-cli 1.0.0\\n'\n"
	writeExe(t, filepath.Join(localBin, "codex"), plain)
	env = []string{"PATH=" + localBin + ":" + bin + ":/usr/bin:/bin", "HOME=" + home}
	apply, err = ApplyScript([]Tool{{Name: "codex", Binary: "codex"}}, []string{localBin, bin}, []StageStep{step})
	if err != nil {
		t.Fatal(err)
	}
	out = runSh(t, apply, env)
	results, _, err = ParseApply(out)
	if err != nil {
		t.Fatalf("plain apply: %v\n%s", err, out)
	}
	if len(results) != 1 || !strings.Contains(results[0].Blocked, "not a standalone Codex install") {
		t.Fatalf("plain codex should be refused: %+v\n%s", results, out)
	}
	body, err = os.ReadFile(mise)
	if err != nil || string(body) != "MISE-BINARY\n" {
		t.Fatalf("mise binary changed: %v %q", err, body)
	}
	live, err := os.ReadFile(filepath.Join(localBin, "codex"))
	if err != nil || string(live) != plain {
		t.Fatalf("plain codex was rewritten: %v %q", err, live)
	}
}

func TestCursorAgentKeychainDoesNotRollBack(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	localBin := filepath.Join(home, ".local", "bin")
	oldDir := filepath.Join(home, ".local", "share", "cursor-agent", "versions", "2026.01.01-aaaa")
	if err := os.MkdirAll(oldDir, 0o755); err != nil || os.MkdirAll(localBin, 0o755) != nil {
		t.Fatal(err)
	}
	writeExe(t, filepath.Join(oldDir, "cursor-agent"), "#!/bin/sh\nprintf '2026.01.01-aaaa\\n'\n")
	link := filepath.Join(localBin, "agent")
	if err := os.Symlink(filepath.Join(oldDir, "cursor-agent"), link); err != nil {
		t.Fatal(err)
	}
	ver := "2026.10.22-27dcf5f"
	newDir := filepath.Join(home, ".local", "share", "cursor-agent", "versions", ver)
	archive := filepath.Join(root, "agent.tgz")
	writeTarGz(t, archive, map[string]string{
		"payload/cursor-agent": "#!/bin/sh\nprintf '%s\\n' 'Your macOS login keychain is locked' >&2\nexit 1\n",
	})
	cache := filepath.Join(home, ".cache", "clifleet", "staged")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	stamp := "release\tcursor-agent\t" + ver + "\t" + archive + "\n"
	if err := os.WriteFile(filepath.Join(cache, "agent"), []byte(stamp), 0o644); err != nil {
		t.Fatal(err)
	}
	step := StageStep{Tool: "agent", Kind: catalog.KindRelease, Release: "agent"}
	apply, err := ApplyScript([]Tool{{Name: "agent", Binary: "agent"}}, []string{localBin}, []StageStep{step})
	if err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + localBin + ":/usr/bin:/bin", "HOME=" + home}
	out := runSh(t, apply, env)
	results, _, err := ParseApply(out)
	if err != nil {
		t.Fatalf("keychain apply: %v\n%s", err, out)
	}
	if len(results) != 1 || results[0].Exit != 0 || !strings.Contains(results[0].Detail, "keychain") {
		t.Fatalf("%+v\n%s", results, out)
	}
	got, err := os.Readlink(link)
	if err != nil || !strings.Contains(got, "/versions/"+ver+"/cursor-agent") {
		t.Fatalf("keychain noise restored the symlink: %s %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(oldDir, "cursor-agent")); err != nil {
		t.Fatalf("old agent removed: %v", err)
	}

	// A real version token that is not the staged release still rolls back.
	if err := os.RemoveAll(newDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(oldDir, "cursor-agent"), link); err != nil {
		t.Fatal(err)
	}
	writeTarGz(t, archive, map[string]string{
		"payload/cursor-agent": "#!/bin/sh\nprintf '2026.01.01-aaaa\\n'\n",
	})
	out = runSh(t, apply, env)
	results, _, err = ParseApply(out)
	if err != nil {
		t.Fatalf("mismatch apply: %v\n%s", err, out)
	}
	if len(results) != 1 || results[0].Exit == 0 || !strings.Contains(results[0].Detail, "restored") {
		t.Fatalf("%+v\n%s", results, out)
	}
	got, err = os.Readlink(link)
	if err != nil || !strings.Contains(got, "/versions/2026.01.01-aaaa/cursor-agent") {
		t.Fatalf("mismatch left the new symlink: %s %v", got, err)
	}
}

func TestBrewUpdateFailureStillFetches(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil || os.MkdirAll(home, 0o755) != nil {
		t.Fatal(err)
	}
	writeExe(t, filepath.Join(bin, "brew"), `#!/bin/sh
if [ "$1" = update ]; then
  printf '%s\n' 'Error: failed to update tap' >&2
  exit 1
fi
if [ "$1" = fetch ]; then
  exit 0
fi
exit 1
`)
	fetch, err := FetchScript([]string{bin}, []StageStep{{
		Tool: "opencode", Kind: catalog.MethodBrew, BrewKind: "formula", BrewToken: "opencode",
	}})
	if err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + home}
	out := runSh(t, fetch, env)
	results, err := ParseFetch(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(results) != 1 || results[0].Exit != 0 || results[0].Blocked != "" {
		t.Fatalf("fetch should survive a failed update: %+v\n%s", results, out)
	}
	if !strings.Contains(results[0].Detail, "existing tap") {
		t.Fatalf("detail %+v", results[0])
	}

	writeExe(t, filepath.Join(bin, "brew"), `#!/bin/sh
printf '%s\n' 'Error: Your Command Line Tools are too outdated.' >&2
exit 1
`)
	out = runSh(t, fetch, env)
	results, err = ParseFetch(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(results) != 1 || results[0].Blocked == "" || !strings.Contains(results[0].Blocked, "Command Line Tools") {
		t.Fatalf("CLT should be blocked, not a bare failure: %+v\n%s", results, out)
	}

	writeExe(t, filepath.Join(bin, "brew"), `#!/bin/sh
if [ "$1" = update ]; then exit 0; fi
printf '%s\n' 'Error: Your Xcode (16.2) is too outdated.' >&2
exit 1
`)
	out = runSh(t, fetch, env)
	results, err = ParseFetch(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(results) != 1 || results[0].Blocked == "" || !strings.Contains(results[0].Blocked, "Xcode") {
		t.Fatalf("Xcode should be blocked: %+v\n%s", results, out)
	}
}

func TestReleaseFetchLimitTracksToolBudget(t *testing.T) {
	step := StageStep{Tool: "codex", Kind: catalog.KindRelease, Release: "codex"}
	fetch, err := FetchScript(nil, []StageStep{step})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fetch, "cf_limit 480 ") {
		t.Fatal("default release fetch cap should be 480s")
	}
	long, err := FetchScriptBudget(nil, []StageStep{step}, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(long, "cf_limit 580 ") {
		t.Fatal("10m host budget should raise the release fetch cap to 580s")
	}
	brew, err := FetchScript(nil, []StageStep{{
		Tool: "opencode", Kind: catalog.MethodBrew, BrewKind: "formula", BrewToken: "opencode",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(brew, "cf_limit 180 stage_brew") {
		t.Fatal("default brew fetch cap should stay 180s")
	}
	apply, err := ApplyScript([]Tool{{Name: "codex", Binary: "codex"}}, nil, []StageStep{step})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(apply, "cf_limit 180 ") {
		t.Fatal("release apply should stay a short swap")
	}
}

func writeTarGz(t *testing.T, path string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	for name, body := range files {
		mode := int64(0o644)
		if strings.Contains(name, "bin/") || strings.HasSuffix(name, "/codex") || name == "bin/codex" {
			mode = 0o755
		}
		hdr := &tar.Header{Name: name, Mode: mode, Size: int64(len(body))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
}

// npmRollbackFixture installs codex 1.0.0, with a darwin-arm64 platform
// package nested under it (where npm puts it) when withPlat is set, stages
// 2.0.0 (main plus platform) whose codex.js is newCodex, and runs apply. The
// staged platform tarball is named like the real one: package.json says
// "@openai/codex", version "2.0.0-darwin-arm64". It returns the result and
// the main package, nested platform package and prefix directories.
func npmRollbackFixture(t *testing.T, newCodex string, withPlat bool) (ActionResult, string, string, string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	bin := filepath.Join(root, "bin")
	prefix := filepath.Join(root, "prefix")
	liveRoot := filepath.Join(prefix, "lib", "node_modules")
	mainDir := filepath.Join(liveRoot, "@openai", "codex")
	platDir := filepath.Join(mainDir, "node_modules", "@openai", "codex-darwin-arm64")
	cache := filepath.Join(home, ".cache", "clifleet")
	for _, d := range []string{filepath.Join(mainDir, "bin"), bin, filepath.Join(prefix, "bin"), filepath.Join(cache, "npm", "codex"), filepath.Join(cache, "staged")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeExe(t, filepath.Join(mainDir, "bin", "codex.js"), "#!/bin/sh\nprintf 'codex-cli 1.0.0\\n'\n")
	if err := os.WriteFile(filepath.Join(mainDir, "package.json"), []byte("{\"name\":\"@openai/codex\",\"version\":\"1.0.0\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if withPlat {
		if err := os.MkdirAll(platDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(platDir, "id"), []byte("OLD-plat\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("../lib/node_modules/@openai/codex/bin/codex.js", filepath.Join(prefix, "bin", "codex")); err != nil {
		t.Fatal(err)
	}
	tarball := filepath.Join(cache, "npm", "codex", "openai-codex-2.0.0.tgz")
	writeTarGz(t, tarball, map[string]string{"package/package.json": "{\"name\":\"@openai/codex\",\"version\":\"2.0.0\"}\n"})
	platTarball := filepath.Join(cache, "npm", "codex", "openai-codex-2.0.0-darwin-arm64.tgz")
	writeTarGz(t, platTarball, map[string]string{
		"package/package.json": "{\"name\":\"@openai/codex\",\"version\":\"2.0.0-darwin-arm64\"}\n",
		"package/id":           "NEW-plat\n",
	})
	stamp := "npm\t@openai/codex\t" + tarball + "\t2.0.0\t-\t" + platTarball + "\n"
	if err := os.WriteFile(filepath.Join(cache, "staged", "codex"), []byte(stamp), 0o644); err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(root, "new-codex.js")
	if err := os.WriteFile(newPath, []byte(newCodex), 0o644); err != nil {
		t.Fatal(err)
	}
	// npm installs only the main package; apply nests the staged platform.
	writeExe(t, filepath.Join(bin, "npm"), `#!/bin/sh
if [ "$1" = root ]; then
  printf '%s\n' "$LIVE_ROOT"
  exit 0
fi
if [ "$1" = install ]; then
  printf '%s\n' "$*" >> "$NPM_LOG"
  p=.
  last=
  for a in "$@"; do
    if [ "$last" = --prefix ]; then p=$a; fi
    last=$a
  done
  mkdir -p "$p/lib/node_modules/@openai/codex/bin"
  printf '%s\n' '{"name":"@openai/codex","version":"2.0.0"}' > "$p/lib/node_modules/@openai/codex/package.json"
  cp "$NEW_CODEX" "$p/lib/node_modules/@openai/codex/bin/codex.js"
  chmod 755 "$p/lib/node_modules/@openai/codex/bin/codex.js"
  exit 0
fi
exit 0
`)
	script, err := ApplyScript([]Tool{{Name: "codex", Binary: "codex"}}, []string{filepath.Join(prefix, "bin"), bin}, []StageStep{{
		Tool: "codex", Kind: catalog.MethodNpm, Package: "@openai/codex",
	}})
	if err != nil {
		t.Fatal(err)
	}
	env := []string{
		"PATH=" + filepath.Join(prefix, "bin") + ":" + bin + ":/usr/bin:/bin",
		"HOME=" + home,
		"LIVE_ROOT=" + liveRoot,
		"NEW_CODEX=" + newPath,
		"NPM_LOG=" + filepath.Join(root, "npm.log"),
	}
	out := runSh(t, script, env)
	results, _, err := ParseApply(out)
	if err != nil || len(results) != 1 {
		t.Fatalf("%v %+v\n%s", err, results, out)
	}
	logged, _ := os.ReadFile(filepath.Join(root, "npm.log"))
	if !strings.Contains(string(logged), "--omit=optional") || strings.Contains(string(logged), "darwin-arm64.tgz") {
		t.Fatalf("npm must install only the main tarball, without optional deps:\n%s", logged)
	}
	return results[0], mainDir, platDir, prefix
}

// When the new codex does not run, both the main package and its platform
// package go back to the previous install. Each must land in its own place.
func TestNpmRollbackRestoresMainAndPlatform(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	got, mainDir, platDir, prefix := npmRollbackFixture(t, "#!/bin/sh\nexit 1\n", true)
	if got.Exit == 0 || !strings.Contains(got.Detail, "restored") {
		t.Fatalf("apply %+v", got)
	}
	ver, err := os.ReadFile(filepath.Join(mainDir, "package.json"))
	if err != nil || !strings.Contains(string(ver), "1.0.0") {
		t.Fatalf("main package not restored: %v %q", err, ver)
	}
	plat, err := os.ReadFile(filepath.Join(platDir, "id"))
	if err != nil || string(plat) != "OLD-plat\n" {
		t.Fatalf("platform package not restored: %v %q", err, plat)
	}
	still, err := exec.Command(filepath.Join(prefix, "bin", "codex"), "--version").Output()
	if err != nil || !strings.Contains(string(still), "1.0.0") {
		t.Fatalf("codex after rollback %v %q", err, still)
	}
}

// A staged release that does not run, or reports another version, must not
// stay live. Before this, a binary killed on exec counted as "did not print a
// version" and the swap was reported as a success.
func TestReleaseApplyRollsBackBrokenBinary(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	setup := func(t *testing.T) (home, localBin string, env []string) {
		root := t.TempDir()
		home = filepath.Join(root, "home")
		localBin = filepath.Join(home, ".local", "bin")
		if err := os.MkdirAll(localBin, 0o755); err != nil || os.MkdirAll(filepath.Join(home, ".cache", "clifleet", "staged"), 0o755) != nil {
			t.Fatal(err)
		}
		return home, localBin, []string{"PATH=" + localBin + ":/usr/bin:/bin", "HOME=" + home}
	}
	stage := func(t *testing.T, home, tool, line string) {
		if err := os.WriteFile(filepath.Join(home, ".cache", "clifleet", "staged", tool), []byte(line+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	apply := func(t *testing.T, tool string, extra []string, env []string) ActionResult {
		script, err := ApplyScript([]Tool{{Name: tool, Binary: tool}}, extra, []StageStep{{Tool: tool, Kind: catalog.KindRelease, Release: tool}})
		if err != nil {
			t.Fatal(err)
		}
		out := runSh(t, script, env)
		results, _, err := ParseApply(out)
		if err != nil || len(results) != 1 {
			t.Fatalf("apply %v %+v\n%s", err, results, out)
		}
		if results[0].Exit == 0 || !strings.Contains(results[0].Detail, "restored") {
			t.Fatalf("broken release was accepted: %+v\n%s", results[0], out)
		}
		return results[0]
	}
	version := func(t *testing.T, path string) string {
		out, err := exec.Command(path, "--version").Output()
		if err != nil {
			t.Fatalf("%s --version: %v", path, err)
		}
		return string(out)
	}
	const crash = "#!/bin/sh\nkill -9 $$\n"

	t.Run("claude symlink", func(t *testing.T) {
		home, localBin, env := setup(t)
		versions := filepath.Join(home, ".local", "share", "claude", "versions")
		if err := os.MkdirAll(versions, 0o755); err != nil {
			t.Fatal(err)
		}
		old := filepath.Join(versions, "2.1.0")
		writeExe(t, old, "#!/bin/sh\nprintf '2.1.0 (Claude Code)\\n'\n")
		if err := os.Symlink(old, filepath.Join(localBin, "claude")); err != nil {
			t.Fatal(err)
		}
		staged := filepath.Join(home, ".cache", "clifleet", "claude-2.1.40")
		writeExe(t, staged, crash)
		stage(t, home, "claude", "release\tclaude\t2.1.40\t"+staged)
		got := apply(t, "claude", []string{localBin}, env)
		if !strings.Contains(got.Detail, "did not run") {
			t.Fatalf("detail %q", got.Detail)
		}
		if link, _ := os.Readlink(filepath.Join(localBin, "claude")); link != old {
			t.Fatalf("claude link %q, want %q", link, old)
		}
	})

	t.Run("claude prints the version then dies", func(t *testing.T) {
		home, localBin, env := setup(t)
		versions := filepath.Join(home, ".local", "share", "claude", "versions")
		if err := os.MkdirAll(versions, 0o755); err != nil {
			t.Fatal(err)
		}
		old := filepath.Join(versions, "2.1.0")
		writeExe(t, old, "#!/bin/sh\nprintf '2.1.0 (Claude Code)\\n'\n")
		if err := os.Symlink(old, filepath.Join(localBin, "claude")); err != nil {
			t.Fatal(err)
		}
		staged := filepath.Join(home, ".cache", "clifleet", "claude-2.1.40")
		writeExe(t, staged, "#!/bin/sh\nprintf '2.1.40 (Claude Code)\\n'\nkill -9 $$\n")
		stage(t, home, "claude", "release\tclaude\t2.1.40\t"+staged)
		got := apply(t, "claude", []string{localBin}, env)
		if !strings.Contains(got.Detail, "did not run") {
			t.Fatalf("detail %q", got.Detail)
		}
		if link, _ := os.Readlink(filepath.Join(localBin, "claude")); link != old {
			t.Fatalf("claude link %q, want %q", link, old)
		}
	})

	t.Run("codex without a current link", func(t *testing.T) {
		home, localBin, env := setup(t)
		standalone := filepath.Join(home, ".codex", "packages", "standalone")
		oldRel := filepath.Join(standalone, "releases", "1.0.0-old")
		if err := os.MkdirAll(filepath.Join(oldRel, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		oldBin := filepath.Join(oldRel, "bin", "codex")
		writeExe(t, oldBin, "#!/bin/sh\nprintf 'codex-cli 1.0.0\\n'\n")
		if err := os.Symlink(oldBin, filepath.Join(localBin, "codex")); err != nil {
			t.Fatal(err)
		}
		archive := filepath.Join(home, ".cache", "clifleet", "codex-package.tgz")
		writeTarGz(t, archive, map[string]string{
			"bin/codex":          crash,
			"codex-package.json": "{\"version\":\"1.2.3\"}\n",
		})
		stage(t, home, "codex", "release\tcodex\tcodex-package.tgz\t"+archive)
		apply(t, "codex", []string{localBin}, env)
		if _, err := os.Lstat(filepath.Join(standalone, "current")); err == nil {
			t.Fatal("rollback left a current link that did not exist before")
		}
		if link, _ := os.Readlink(filepath.Join(localBin, "codex")); link != oldBin {
			t.Fatalf("codex link %q, want %q", link, oldBin)
		}
	})

	t.Run("codex standalone", func(t *testing.T) {
		home, localBin, env := setup(t)
		standalone := filepath.Join(home, ".codex", "packages", "standalone")
		oldRel := filepath.Join(standalone, "releases", "1.0.0-old")
		if err := os.MkdirAll(filepath.Join(oldRel, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeExe(t, filepath.Join(oldRel, "bin", "codex"), "#!/bin/sh\nprintf 'codex-cli 1.0.0\\n'\n")
		if err := os.Symlink(oldRel, filepath.Join(standalone, "current")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(standalone, "current", "bin", "codex"), filepath.Join(localBin, "codex")); err != nil {
			t.Fatal(err)
		}
		// The package says 1.2.3 but the binary inside is something else.
		archive := filepath.Join(home, ".cache", "clifleet", "codex-package.tgz")
		writeTarGz(t, archive, map[string]string{
			"bin/codex":          "#!/bin/sh\nprintf 'codex-cli 9.9.9\\n'\n",
			"codex-package.json": "{\"version\":\"1.2.3\"}\n",
		})
		stage(t, home, "codex", "release\tcodex\tcodex-package.tgz\t"+archive)
		got := apply(t, "codex", []string{localBin}, env)
		if !strings.Contains(got.Detail, "reported 9.9.9, want 1.2.3") {
			t.Fatalf("detail %q", got.Detail)
		}
		if cur, _ := os.Readlink(filepath.Join(standalone, "current")); cur != oldRel {
			t.Fatalf("current link %q, want %q", cur, oldRel)
		}
		if v := version(t, filepath.Join(localBin, "codex")); !strings.Contains(v, "1.0.0") {
			t.Fatalf("codex after rollback %q", v)
		}
	})
}

// A rollback that cannot put the old link back must say so instead of
// reporting "restored".
func TestReleaseRollbackFailureIsReported(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores the read-only directory")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	localBin := filepath.Join(home, ".local", "bin")
	versions := filepath.Join(home, ".local", "share", "claude", "versions")
	staged := filepath.Join(home, ".cache", "clifleet", "staged")
	for _, d := range []string{localBin, versions, staged} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = os.Chmod(localBin, 0o755) })
	old := filepath.Join(versions, "2.1.0")
	writeExe(t, old, "#!/bin/sh\nprintf '2.1.0 (Claude Code)\\n'\n")
	if err := os.Symlink(old, filepath.Join(localBin, "claude")); err != nil {
		t.Fatal(err)
	}
	// The new binary locks the link directory and then dies.
	bad := filepath.Join(home, ".cache", "clifleet", "claude-2.1.40")
	writeExe(t, bad, "#!/bin/sh\nchmod 555 \"$HOME/.local/bin\"\nkill -9 $$\n")
	if err := os.WriteFile(filepath.Join(staged, "claude"), []byte("release\tclaude\t2.1.40\t"+bad+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	script, err := ApplyScript([]Tool{{Name: "claude", Binary: "claude"}}, []string{localBin}, []StageStep{{Tool: "claude", Kind: catalog.KindRelease, Release: "claude"}})
	if err != nil {
		t.Fatal(err)
	}
	out := runSh(t, script, []string{"PATH=" + localBin + ":/usr/bin:/bin", "HOME=" + home})
	results, _, err := ParseApply(out)
	if err != nil || len(results) != 1 {
		t.Fatalf("%v %+v\n%s", err, results, out)
	}
	got := results[0]
	if got.Exit == 0 || strings.Contains(got.Detail, "restored") || !strings.Contains(got.Detail, "rollback failed") {
		t.Fatalf("detail %+v\n%s", got, out)
	}
}

// cursorFixture lays out a Cursor Agent install at oldVer with the given
// aliases in ~/.local/bin, and stages newVer whose binary is body.
func cursorFixture(t *testing.T, tool, oldVer, newVer, body string, aliases ...string) (home, localBin string, env []string) {
	t.Helper()
	root := t.TempDir()
	home = filepath.Join(root, "home")
	localBin = filepath.Join(home, ".local", "bin")
	oldDir := filepath.Join(home, ".local", "share", "cursor-agent", "versions", oldVer)
	staged := filepath.Join(home, ".cache", "clifleet", "staged")
	for _, d := range []string{localBin, oldDir, staged} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeExe(t, filepath.Join(oldDir, "cursor-agent"), "#!/bin/sh\nprintf '"+oldVer+"\\n'\n")
	for _, a := range aliases {
		if err := os.Symlink(filepath.Join(oldDir, "cursor-agent"), filepath.Join(localBin, a)); err != nil {
			t.Fatal(err)
		}
	}
	archive := filepath.Join(root, "agent.tgz")
	writeTarGz(t, archive, map[string]string{"payload/cursor-agent": body})
	if err := os.WriteFile(filepath.Join(staged, tool), []byte("release\tcursor-agent\t"+newVer+"\t"+archive+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return home, localBin, []string{"PATH=" + localBin + ":/usr/bin:/bin", "HOME=" + home}
}

func applyOneRelease(t *testing.T, tool, release, extra string, env []string) ActionResult {
	t.Helper()
	script, err := ApplyScript([]Tool{{Name: tool, Binary: tool}}, []string{extra}, []StageStep{{Tool: tool, Kind: catalog.KindRelease, Release: release}})
	if err != nil {
		t.Fatal(err)
	}
	out := runSh(t, script, env)
	results, _, err := ParseApply(out)
	if err != nil || len(results) != 1 {
		t.Fatalf("%v %+v\n%s", err, results, out)
	}
	return results[0]
}

// Another program called agent in ~/.local/bin is not Cursor's alias.
func TestCursorAgentLeavesForeignAgentAlone(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	ver := "2026.10.28-abc123"
	home, localBin, env := cursorFixture(t, "cursor-agent", "2026.01.01-aaaa", ver, "#!/bin/sh\nprintf '"+ver+"\\n'\n", "cursor-agent")
	other := filepath.Join(home, "other", "agent")
	if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
		t.Fatal(err)
	}
	writeExe(t, other, "#!/bin/sh\necho other\n")
	if err := os.Symlink(other, filepath.Join(localBin, "agent")); err != nil {
		t.Fatal(err)
	}
	got := applyOneRelease(t, "cursor-agent", "cursor_agent", localBin, env)
	if got.Exit != 0 {
		t.Fatalf("apply %+v", got)
	}
	if link, _ := os.Readlink(filepath.Join(localBin, "agent")); link != other {
		t.Fatalf("foreign agent retargeted to %q", link)
	}
	if link, _ := os.Readlink(filepath.Join(localBin, "cursor-agent")); !strings.Contains(link, "/versions/"+ver+"/") {
		t.Fatalf("cursor-agent link %q", link)
	}
}

// When the live link already points at the staged version but its version
// check printed nothing, apply must not rewrite the files it runs from.
func TestReleaseApplyKeepsAlreadyLinkedVersion(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	const silent = "#!/bin/sh\nexit 0\n"
	t.Run("claude", func(t *testing.T) {
		root := t.TempDir()
		home := filepath.Join(root, "home")
		localBin := filepath.Join(home, ".local", "bin")
		versions := filepath.Join(home, ".local", "share", "claude", "versions")
		staged := filepath.Join(home, ".cache", "clifleet", "staged")
		for _, d := range []string{localBin, versions, staged} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		live := filepath.Join(versions, "2.1.40")
		writeExe(t, live, silent)
		if err := os.Symlink(live, filepath.Join(localBin, "claude")); err != nil {
			t.Fatal(err)
		}
		other := filepath.Join(home, ".cache", "clifleet", "claude-2.1.40")
		writeExe(t, other, "#!/bin/sh\nkill -9 $$\n")
		if err := os.WriteFile(filepath.Join(staged, "claude"), []byte("release\tclaude\t2.1.40\t"+other+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := applyOneRelease(t, "claude", "claude", localBin, []string{"PATH=" + localBin + ":/usr/bin:/bin", "HOME=" + home})
		if !got.Current {
			t.Fatalf("apply %+v", got)
		}
		if body, _ := os.ReadFile(live); string(body) != silent {
			t.Fatalf("live claude rewritten: %q", body)
		}
	})
	t.Run("cursor agent", func(t *testing.T) {
		ver := "2026.10.28-abc123"
		home, localBin, env := cursorFixture(t, "agent", ver, ver, "#!/bin/sh\nkill -9 $$\n", "agent")
		liveBin := filepath.Join(home, ".local", "share", "cursor-agent", "versions", ver, "cursor-agent")
		writeExe(t, liveBin, silent)
		got := applyOneRelease(t, "agent", "agent", localBin, env)
		if !got.Current {
			t.Fatalf("apply %+v", got)
		}
		if body, _ := os.ReadFile(liveBin); string(body) != silent {
			t.Fatalf("live agent rewritten: %q", body)
		}
	})
}

// The native installer may write a relative link; it is still the vendor layout.
func TestClaudeRelativeLinkIsVendorLayout(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	localBin := filepath.Join(home, ".local", "bin")
	versions := filepath.Join(home, ".local", "share", "claude", "versions")
	staged := filepath.Join(home, ".cache", "clifleet", "staged")
	for _, d := range []string{localBin, versions, staged} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeExe(t, filepath.Join(versions, "2.1.0"), "#!/bin/sh\nprintf '2.1.0 (Claude Code)\\n'\n")
	if err := os.Symlink("../share/claude/versions/2.1.0", filepath.Join(localBin, "claude")); err != nil {
		t.Fatal(err)
	}
	probe, err := ProbeScript([]Tool{{Name: "claude", Binary: "claude"}}, []string{localBin})
	if err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + localBin + ":/usr/bin:/bin", "HOME=" + home}
	out := runSh(t, probe, env)
	obs, err := ParseProbe(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if real := obs["claude"].Real; strings.Contains(real, "..") || !strings.Contains(real, "/.local/share/claude/versions/2.1.0") {
		t.Fatalf("real %q\n%s", real, out)
	}
	newBin := filepath.Join(home, ".cache", "clifleet", "claude-2.1.40")
	writeExe(t, newBin, "#!/bin/sh\nprintf '2.1.40 (Claude Code)\\n'\n")
	if err := os.WriteFile(filepath.Join(staged, "claude"), []byte("release\tclaude\t2.1.40\t"+newBin+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := applyOneRelease(t, "claude", "claude", localBin, env)
	if got.Exit != 0 || got.Blocked != "" {
		t.Fatalf("apply %+v", got)
	}
}

// A new package that cannot run and also blocks the rollback must not be
// reported as restored.
func TestNpmRollbackFailureIsReported(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores the read-only directory")
	}
	// The new codex.js locks the @openai scope dir, so the old package cannot
	// be moved back. Cleanup runs before TempDir's removal (LIFO).
	got, mainDir, _, _ := npmRollbackFixture(t, "#!/bin/sh\nchmod 555 \"$LIVE_ROOT/@openai\"\nexit 1\n", true)
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(mainDir), 0o755) })
	if got.Exit == 0 || strings.Contains(got.Detail, "previous install restored") || !strings.Contains(got.Detail, "rollback failed") {
		t.Fatalf("apply %+v", got)
	}
}

// The real Codex platform tarball names itself "@openai/codex"; it must land
// under the main package as @openai/codex-<platform>, never over the main
// package (seen on a real host: the new codex did not run and rolled back).
func TestNpmCodexPlatformNestsUnderMain(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	got, mainDir, platDir, prefix := npmRollbackFixture(t, "#!/bin/sh\nprintf 'codex-cli 2.0.0\\n'\n", true)
	if got.Exit != 0 {
		t.Fatalf("apply %+v", got)
	}
	if ver, err := os.ReadFile(filepath.Join(mainDir, "package.json")); err != nil || !strings.Contains(string(ver), "\"2.0.0\"") {
		t.Fatalf("main package %v %q", err, ver)
	}
	if id, err := os.ReadFile(filepath.Join(platDir, "id")); err != nil || string(id) != "NEW-plat\n" {
		t.Fatalf("platform package not nested: %v %q", err, id)
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(mainDir), "codex-darwin-arm64")); err == nil {
		t.Fatal("platform package also placed beside the main package")
	}
	out, err := exec.Command(filepath.Join(prefix, "bin", "codex"), "--version").Output()
	if err != nil || !strings.Contains(string(out), "2.0.0") {
		t.Fatalf("codex after apply %v %q", err, out)
	}
}

// A platform package that only this apply created goes away on rollback;
// leaving it would install something that was not there.
func TestNpmRollbackRemovesNewPlatformPackage(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	got, mainDir, platDir, _ := npmRollbackFixture(t, "#!/bin/sh\nexit 1\n", false)
	if got.Exit == 0 || !strings.Contains(got.Detail, "previous install restored") {
		t.Fatalf("apply %+v", got)
	}
	if _, err := os.Lstat(platDir); err == nil {
		t.Fatal("rollback left the new platform package behind")
	}
	if ver, err := os.ReadFile(filepath.Join(mainDir, "package.json")); err != nil || !strings.Contains(string(ver), "1.0.0") {
		t.Fatalf("main package not restored: %v %q", err, ver)
	}
}

// codexTarget mirrors cf_codex_target for the hosts the tests run on.
func codexTarget(t *testing.T) string {
	t.Helper()
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "darwin/arm64":
		return "aarch64-apple-darwin"
	case "linux/amd64":
		return "x86_64-unknown-linux-musl"
	case "linux/arm64":
		return "aarch64-unknown-linux-musl"
	}
	t.Skip("no Codex target mapping for " + runtime.GOOS + "/" + runtime.GOARCH)
	return ""
}

// current already holds the staged release but the PATH link still runs an
// older one: apply moves the link and leaves the release dir alone.
func TestCodexApplyMovesLaggingLink(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	localBin := filepath.Join(home, ".local", "bin")
	standalone := filepath.Join(home, ".codex", "packages", "standalone")
	oldRel := filepath.Join(standalone, "releases", "1.0.0-old")
	newRel := filepath.Join(standalone, "releases", "1.2.3-"+codexTarget(t))
	staged := filepath.Join(home, ".cache", "clifleet", "staged")
	for _, d := range []string{localBin, filepath.Join(oldRel, "bin"), filepath.Join(newRel, "bin"), staged} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeExe(t, filepath.Join(oldRel, "bin", "codex"), "#!/bin/sh\nprintf 'codex-cli 1.0.0\\n'\n")
	const installed = "#!/bin/sh\nprintf 'codex-cli 1.2.3\\n'\n# installed\n"
	writeExe(t, filepath.Join(newRel, "bin", "codex"), installed)
	if err := os.Symlink(newRel, filepath.Join(standalone, "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(oldRel, "bin", "codex"), filepath.Join(localBin, "codex")); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "codex-package.tgz")
	writeTarGz(t, archive, map[string]string{
		"bin/codex":          "#!/bin/sh\nkill -9 $$\n",
		"codex-package.json": "{\"version\":\"1.2.3\"}\n",
	})
	if err := os.WriteFile(filepath.Join(staged, "codex"), []byte("release\tcodex\tcodex-package.tgz\t"+archive+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := applyOneRelease(t, "codex", "codex", localBin, []string{"PATH=" + localBin + ":/usr/bin:/bin", "HOME=" + home})
	if got.Exit != 0 || got.Current {
		t.Fatalf("apply %+v", got)
	}
	if body, _ := os.ReadFile(filepath.Join(newRel, "bin", "codex")); string(body) != installed {
		t.Fatalf("installed release rewritten: %q", body)
	}
	if link, _ := os.Readlink(filepath.Join(localBin, "codex")); link != filepath.Join(standalone, "current", "bin", "codex") {
		t.Fatalf("codex link %q", link)
	}
}

// agent runs an old release while cursor-agent already runs the staged one.
// Upgrading agent must link it to that release, not rewrite it.
func TestCursorAgentKeepsReleaseTheOtherAliasRuns(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	ver := "2026.10.28-abc123"
	home, localBin, env := cursorFixture(t, "agent", "2026.01.01-aaaa", ver, "#!/bin/sh\nkill -9 $$\n", "agent")
	inUse := filepath.Join(home, ".local", "share", "cursor-agent", "versions", ver)
	if err := os.MkdirAll(inUse, 0o755); err != nil {
		t.Fatal(err)
	}
	working := "#!/bin/sh\nprintf '" + ver + "\\n'\n"
	writeExe(t, filepath.Join(inUse, "cursor-agent"), working)
	if err := os.Symlink(filepath.Join(inUse, "cursor-agent"), filepath.Join(localBin, "cursor-agent")); err != nil {
		t.Fatal(err)
	}
	got := applyOneRelease(t, "agent", "agent", localBin, env)
	if got.Exit != 0 {
		t.Fatalf("apply %+v", got)
	}
	if body, _ := os.ReadFile(filepath.Join(inUse, "cursor-agent")); string(body) != working {
		t.Fatalf("release cursor-agent runs was rewritten: %q", body)
	}
	if link, _ := os.Readlink(filepath.Join(localBin, "agent")); !strings.Contains(link, "/versions/"+ver+"/") {
		t.Fatalf("agent link %q", link)
	}
}

// Rolling back a lagging-link apply only moves the PATH link back. current was
// never changed, so a read-only standalone dir must not turn into a failed
// rollback.
func TestCodexLaggingLinkRollbackLeavesCurrent(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores the read-only directory")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	localBin := filepath.Join(home, ".local", "bin")
	standalone := filepath.Join(home, ".codex", "packages", "standalone")
	oldRel := filepath.Join(standalone, "releases", "1.0.0-old")
	newRel := filepath.Join(standalone, "releases", "1.2.3-"+codexTarget(t))
	staged := filepath.Join(home, ".cache", "clifleet", "staged")
	for _, d := range []string{localBin, filepath.Join(oldRel, "bin"), filepath.Join(newRel, "bin"), staged} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	oldBin := filepath.Join(oldRel, "bin", "codex")
	writeExe(t, oldBin, "#!/bin/sh\nprintf 'codex-cli 1.0.0\\n'\n")
	writeExe(t, filepath.Join(newRel, "bin", "codex"), "#!/bin/sh\nkill -9 $$\n")
	if err := os.Symlink(newRel, filepath.Join(standalone, "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(oldBin, filepath.Join(localBin, "codex")); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "codex-package.tgz")
	writeTarGz(t, archive, map[string]string{
		"bin/codex":          "#!/bin/sh\nkill -9 $$\n",
		"codex-package.json": "{\"version\":\"1.2.3\"}\n",
	})
	if err := os.WriteFile(filepath.Join(staged, "codex"), []byte("release\tcodex\tcodex-package.tgz\t"+archive+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(standalone, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(standalone, 0o755) })
	got := applyOneRelease(t, "codex", "codex", localBin, []string{"PATH=" + localBin + ":/usr/bin:/bin", "HOME=" + home})
	if got.Exit == 0 || !strings.Contains(got.Detail, "previous release restored") {
		t.Fatalf("apply %+v", got)
	}
	if link, _ := os.Readlink(filepath.Join(localBin, "codex")); link != oldBin {
		t.Fatalf("codex link %q, want %q", link, oldBin)
	}
}

// npm fetch grows with the host budget like brew and release; a fixed
// 120 s cut the ~150 MB npm Codex platform package on a shared link.
func TestNpmFetchLimitTracksToolBudget(t *testing.T) {
	if got := limitSeconds(true, catalog.MethodNpm, 10*time.Minute); got != 580 {
		t.Fatalf("npm fetch cap under a 10m budget: %ds", got)
	}
	if got := limitSeconds(true, catalog.MethodNpm, 0); got != 120 {
		t.Fatalf("npm fetch cap without a budget: %ds", got)
	}
	if got := limitSeconds(false, catalog.MethodNpm, 10*time.Minute); got != 580 {
		t.Fatalf("npm apply cap under a 10m budget: %ds", got)
	}
	if got := limitSeconds(false, catalog.KindRelease, 10*time.Minute); got != 180 {
		t.Fatalf("release apply cap moved: %ds", got)
	}
}

// The remote cap must stay under a short host budget, or SSH kills the
// script before it can report "timed out".
func TestLimitSecondsStaysUnderShortBudget(t *testing.T) {
	for _, budget := range []time.Duration{2 * time.Second, 30 * time.Second, 59 * time.Second, 65 * time.Second} {
		for _, fetch := range []bool{true, false} {
			for _, kind := range []string{catalog.MethodNpm, catalog.MethodBrew, catalog.KindRelease} {
				if got := limitSeconds(fetch, kind, budget); got < 1 || got >= int(budget.Seconds()) {
					t.Errorf("budget %s fetch=%v %s: cap %ds", budget, fetch, kind, got)
				}
			}
		}
	}
}

// The new version moved its bin and added another. After a rollback the old
// link target is back and the added bin is gone, instead of links into the
// new layout that dangle against the restored package.
func TestNpmRollbackRestoresBinLinks(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	bin := filepath.Join(root, "bin")
	prefix := filepath.Join(root, "prefix")
	liveRoot := filepath.Join(prefix, "lib", "node_modules")
	mainDir := filepath.Join(liveRoot, "@openai", "codex")
	cache := filepath.Join(home, ".cache", "clifleet")
	for _, d := range []string{filepath.Join(mainDir, "bin"), bin, filepath.Join(prefix, "bin"), filepath.Join(cache, "npm", "codex"), filepath.Join(cache, "staged")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeExe(t, filepath.Join(mainDir, "bin", "codex.js"), "#!/bin/sh\nprintf 'codex-cli 1.0.0\\n'\n")
	if err := os.WriteFile(filepath.Join(mainDir, "package.json"), []byte("{\"name\":\"@openai/codex\",\"version\":\"1.0.0\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const oldLink = "../lib/node_modules/@openai/codex/bin/codex.js"
	if err := os.Symlink(oldLink, filepath.Join(prefix, "bin", "codex")); err != nil {
		t.Fatal(err)
	}
	// Someone else's file named like a new bin must survive both the link and the undo.
	writeExe(t, filepath.Join(prefix, "bin", "codex-mcp"), "#!/bin/sh\necho mine\n")
	tarball := filepath.Join(cache, "npm", "codex", "openai-codex-2.0.0.tgz")
	writeTarGz(t, tarball, map[string]string{"package/package.json": "{\"name\":\"@openai/codex\",\"version\":\"2.0.0\"}\n"})
	if err := os.WriteFile(filepath.Join(cache, "staged", "codex"), []byte("npm\t@openai/codex\t"+tarball+"\t2.0.0\t-\t-\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeExe(t, filepath.Join(bin, "npm"), `#!/bin/sh
if [ "$1" = root ]; then
  printf '%s\n' "$LIVE_ROOT"
  exit 0
fi
if [ "$1" = install ]; then
  p=.
  last=
  for a in "$@"; do
    if [ "$last" = --prefix ]; then p=$a; fi
    last=$a
  done
  d=$p/lib/node_modules/@openai/codex
  mkdir -p "$d/dist"
  printf '%s\n' '{"name":"@openai/codex","version":"2.0.0"}' > "$d/package.json"
  printf 'codex\tdist/codex-v2.js\ncodex-extra\tdist/extra.js\ncodex-mcp\tdist/mcp.js\n' > "$d/bins.txt"
  printf '#!/bin/sh\nexit 1\n' > "$d/dist/codex-v2.js"
  printf '#!/bin/sh\nexit 0\n' > "$d/dist/extra.js"
  printf '#!/bin/sh\nexit 0\n' > "$d/dist/mcp.js"
  chmod 755 "$d/dist/codex-v2.js" "$d/dist/extra.js" "$d/dist/mcp.js"
  exit 0
fi
exit 0
`)
	// A stand-in for node's bin-map read: the real one lives beside real CLIs.
	writeExe(t, filepath.Join(bin, "node"), `#!/bin/sh
case $2 in
  *'process.exit(0)'*) exit 0 ;;
esac
cat "$(dirname "$CF_JSON")/bins.txt"
`)
	script, err := ApplyScript([]Tool{{Name: "codex", Binary: "codex"}}, []string{filepath.Join(prefix, "bin"), bin}, []StageStep{{
		Tool: "codex", Kind: catalog.MethodNpm, Package: "@openai/codex",
	}})
	if err != nil {
		t.Fatal(err)
	}
	out := runSh(t, script, []string{
		"PATH=" + filepath.Join(prefix, "bin") + ":" + bin + ":/usr/bin:/bin",
		"HOME=" + home,
		"LIVE_ROOT=" + liveRoot,
	})
	results, _, err := ParseApply(out)
	if err != nil || len(results) != 1 {
		t.Fatalf("%v %+v\n%s", err, results, out)
	}
	if results[0].Exit == 0 || !strings.Contains(results[0].Detail, "restored") {
		t.Fatalf("apply %+v\n%s", results[0], out)
	}
	if link, err := os.Readlink(filepath.Join(prefix, "bin", "codex")); err != nil || link != oldLink {
		t.Fatalf("codex link %q %v, want %q", link, err, oldLink)
	}
	if _, err := os.Lstat(filepath.Join(prefix, "bin", "codex-extra")); !os.IsNotExist(err) {
		t.Fatalf("bin added by the new version survived the rollback: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(prefix, "bin", "codex-mcp")); err != nil || !strings.Contains(string(body), "mine") {
		t.Fatalf("foreign codex-mcp changed: %v %q", err, body)
	}
	still, err := exec.Command(filepath.Join(prefix, "bin", "codex"), "--version").Output()
	if err != nil || !strings.Contains(string(still), "1.0.0") {
		t.Fatalf("codex after rollback %v %q", err, still)
	}
}

// A Volta shim is Volta's; apply must not relink it to a release.
func TestClaudeApplyRefusesVoltaShim(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	voltaBin := filepath.Join(home, ".volta", "bin")
	cache := filepath.Join(home, ".cache", "clifleet")
	if err := os.MkdirAll(voltaBin, 0o755); err != nil || os.MkdirAll(filepath.Join(cache, "staged"), 0o755) != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(voltaBin, "volta-shim")
	const shimBody = "#!/bin/sh\nprintf '2.0.0\\n'\n"
	if err := os.WriteFile(shim, []byte(shimBody), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shim, filepath.Join(voltaBin, "claude")); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(cache, "claude-2.1.40")
	writeExe(t, staged, "#!/bin/sh\nprintf '2.1.40 (Claude Code)\\n'\n")
	if err := os.WriteFile(filepath.Join(cache, "staged", "claude"), []byte("release\tclaude\t2.1.40\t"+staged+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	apply, err := ApplyScript([]Tool{{Name: "claude", Binary: "claude"}}, []string{voltaBin}, []StageStep{{Tool: "claude", Kind: catalog.KindRelease, Release: "claude"}})
	if err != nil {
		t.Fatal(err)
	}
	out := runSh(t, apply, []string{"PATH=" + voltaBin + ":/usr/bin:/bin", "HOME=" + home})
	results, _, err := ParseApply(out)
	if err != nil || len(results) != 1 || !strings.Contains(results[0].Blocked, "managed by volta") {
		t.Fatalf("%v %+v\n%s", err, results, out)
	}
	if link, _ := os.Readlink(filepath.Join(voltaBin, "claude")); link != shim {
		t.Fatalf("volta shim relinked to %q", link)
	}
	if body, _ := os.ReadFile(shim); string(body) != shimBody {
		t.Fatalf("volta-shim changed: %q", body)
	}
}

// A fetch that fails must not leave an earlier fetch's stamp behind: the
// fetch already replaced that stamp's artifact, so apply would open a
// partial download (seen on a real host as "could not extract").
func TestFailedFetchDropsTheOldStamp(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	bin := filepath.Join(root, "bin")
	cache := filepath.Join(root, "xdg")
	if err := os.MkdirAll(bin, 0o755); err != nil || os.MkdirAll(home, 0o755) != nil {
		t.Fatal(err)
	}
	body := filepath.Join(root, "codex-package.tgz")
	writeTarGz(t, body, map[string]string{
		"bin/codex":          "#!/bin/sh\nprintf 'codex-cli 1.2.3\\n'\n",
		"codex-package.json": "{\"version\":\"1.2.3\",\"target\":\"x86_64-unknown-linux-musl\",\"entrypoint\":\"bin/codex\"}\n",
	})
	// The channel answers; the package download fails once CURL_FAIL is set,
	// after leaving part of a file, as an interrupted transfer does.
	writeExe(t, filepath.Join(bin, "curl"), `#!/bin/sh
out=
url=
prev=
for a in "$@"; do
  if [ "$prev" = -o ]; then out=$a; fi
  case $a in http*) url=$a ;; esac
  prev=$a
done
case $url in
  */channels/latest) printf '%s\n' '{"tag_name":"rust-v1.2.3"}' > "$out"; exit 0 ;;
esac
if [ -n "${CURL_FAIL:-}" ]; then
  head -c 64 "$CURL_BODY" > "$out"
  exit 28
fi
cp "$CURL_BODY" "$out"
`)
	writeExe(t, filepath.Join(bin, "npm"), "#!/bin/sh\nexit 1\n")
	writeExe(t, filepath.Join(bin, "brew"), "#!/bin/sh\nexit 1\n")
	standalone := filepath.Join(home, ".codex", "packages", "standalone")
	oldRel := filepath.Join(standalone, "releases", "1.0.0-old")
	if err := os.MkdirAll(filepath.Join(oldRel, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeExe(t, filepath.Join(oldRel, "bin", "codex"), "#!/bin/sh\nprintf 'codex-cli 1.0.0\\n'\n")
	if err := os.Symlink(oldRel, filepath.Join(standalone, "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(standalone, "current", "bin", "codex"), filepath.Join(bin, "codex")); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"PATH=" + bin + ":/usr/bin:/bin",
		"HOME=" + home,
		"XDG_CACHE_HOME=" + cache,
		"CURL_BODY=" + body,
	}
	codex := StageStep{Tool: "codex", Kind: catalog.KindRelease, Release: "codex"}
	fetch, err := FetchScript([]string{bin}, []StageStep{codex})
	if err != nil {
		t.Fatal(err)
	}
	stamp := filepath.Join(cache, "clifleet", "staged", "codex")
	if res, err := ParseFetch(runSh(t, fetch, env)); err != nil || len(res) != 1 || res[0].Exit != 0 {
		t.Fatalf("first fetch %+v %v", res, err)
	}
	if _, err := os.Stat(stamp); err != nil {
		t.Fatalf("first fetch left no stamp: %v", err)
	}

	failEnv := append(append([]string{}, env...), "CURL_FAIL=1")
	out := runSh(t, fetch, failEnv)
	if res, err := ParseFetch(out); err != nil || len(res) != 1 || res[0].Exit == 0 {
		t.Fatalf("failed fetch %+v %v\n%s", res, err, out)
	}
	if _, err := os.Stat(stamp); !os.IsNotExist(err) {
		t.Fatalf("failed fetch kept the old stamp: %v", err)
	}
	apply, err := ApplyScript([]Tool{{Name: "codex", Binary: "codex"}}, []string{bin}, []StageStep{codex})
	if err != nil {
		t.Fatal(err)
	}
	out = runSh(t, apply, env)
	applied, obs, err := ParseApply(out)
	if err != nil || len(applied) != 1 || applied[0].Skip != "nothing staged" {
		t.Fatalf("apply after failed fetch %+v %v\n%s", applied, err, out)
	}
	if ShortVersion(obs["codex"].Version) != "1.0.0" {
		t.Fatalf("codex changed: %+v", obs["codex"])
	}

	// Every other fetch kind drops its stamp the same way.
	others := []StageStep{
		{Tool: "claude", Kind: catalog.KindRelease, Release: "claude"},
		{Tool: "agent", Kind: catalog.KindRelease, Release: "agent"},
		{Tool: "gemini", Kind: catalog.MethodNpm, Package: "@google/gemini-cli"},
		{Tool: "opencode", Kind: catalog.MethodBrew, BrewKind: "formula", BrewToken: "opencode"},
	}
	for _, step := range others {
		stamp := filepath.Join(cache, "clifleet", "staged", step.Tool)
		for _, p := range []string{stamp, stamp + ".tmp"} {
			if err := os.WriteFile(p, []byte("stale\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		script, err := FetchScript([]string{bin}, []StageStep{step})
		if err != nil {
			t.Fatal(err)
		}
		out := runSh(t, script, failEnv)
		if res, err := ParseFetch(out); err != nil || len(res) != 1 || (res[0].Exit == 0 && res[0].Blocked == "") {
			t.Fatalf("%s fetch %+v %v\n%s", step.Tool, res, err, out)
		}
		for _, p := range []string{stamp, stamp + ".tmp"} {
			if _, err := os.Stat(p); !os.IsNotExist(err) {
				t.Fatalf("%s: failed fetch kept %s: %v", step.Tool, p, err)
			}
		}
	}
}

// claudeStampFixture links ~/.local/bin/claude to versions/<live> and stages
// <staged> by writing the stamp and binary directly. front, when set, is a
// read-only directory early on PATH whose claude links to ~/.local/bin/claude
// (a root-owned /usr/local/bin/claude on a real host).
func claudeStampFixture(t *testing.T, live, staged string, front bool) (home, localBin, frontDir string, env []string) {
	t.Helper()
	root := t.TempDir()
	home = filepath.Join(root, "home")
	localBin = filepath.Join(home, ".local", "bin")
	versions := filepath.Join(home, ".local", "share", "claude", "versions")
	cache := filepath.Join(home, ".cache", "clifleet")
	for _, d := range []string{localBin, versions, filepath.Join(cache, "staged"), filepath.Join(cache, "release", "claude")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeExe(t, filepath.Join(versions, live), "#!/bin/sh\nprintf '"+live+" (Claude Code)\\n'\n")
	if err := os.Symlink(filepath.Join(versions, live), filepath.Join(localBin, "claude")); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(cache, "release", "claude", "claude")
	writeExe(t, archive, "#!/bin/sh\nprintf '"+staged+" (Claude Code)\\n'\n")
	if err := os.WriteFile(filepath.Join(cache, "staged", "claude"), []byte("release\tclaude\t"+staged+"\t"+archive+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := localBin + ":/usr/bin:/bin"
	if front {
		frontDir = filepath.Join(root, "usr-local-bin")
		if err := os.MkdirAll(frontDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(localBin, "claude"), filepath.Join(frontDir, "claude")); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(frontDir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(frontDir, 0o755) })
		path = frontDir + ":" + path
	}
	return home, localBin, frontDir, []string{"PATH=" + path, "HOME=" + home}
}

func applyClaude(t *testing.T, extra []string, env []string) (ActionResult, catalog.Observation) {
	t.Helper()
	script, err := ApplyScript([]Tool{{Name: "claude", Binary: "claude"}}, extra, []StageStep{{Tool: "claude", Kind: catalog.KindRelease, Release: "claude"}})
	if err != nil {
		t.Fatal(err)
	}
	out := runSh(t, script, env)
	results, obs, err := ParseApply(out)
	if err != nil || len(results) != 1 {
		t.Fatalf("%v %+v\n%s", err, results, out)
	}
	return results[0], obs["claude"]
}

// A root-owned link in front of ~/.local/bin/claude stays as it is; apply
// retargets the installer's own link behind it.
func TestClaudeApplyRetargetsTheLinkBehindAFrontLink(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	if os.Geteuid() == 0 {
		t.Skip("root can write the read-only front dir")
	}
	_, localBin, frontDir, env := claudeStampFixture(t, "2.1.312", "2.1.314", true)
	got, obs := applyClaude(t, []string{localBin, frontDir}, env) // prepended in order: frontDir ends up first, as /usr/local/bin does on a host
	if got.Exit != 0 || ShortVersion(obs.Version) != "2.1.314" {
		t.Fatalf("apply %+v reprobe %+v", got, obs)
	}
	if link, err := os.Readlink(filepath.Join(localBin, "claude")); err != nil || !strings.HasSuffix(link, "/versions/2.1.314") {
		t.Fatalf("installer link %q %v", link, err)
	}
	if link, err := os.Readlink(filepath.Join(frontDir, "claude")); err != nil || link != filepath.Join(localBin, "claude") {
		t.Fatalf("front link changed: %q %v", link, err)
	}
}

// A stamp older than what runs now (the tool updated itself after the fetch)
// must not downgrade it; the stamp is dropped.
func TestApplyRefusesAnOlderStamp(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	home, localBin, _, env := claudeStampFixture(t, "2.1.314", "2.1.312", false)
	got, obs := applyClaude(t, []string{localBin}, env)
	if got.Exit != 0 || !strings.Contains(got.Skip, "installed 2.1.314 is newer than staged 2.1.312") {
		t.Fatalf("apply %+v", got)
	}
	if ShortVersion(obs.Version) != "2.1.314" {
		t.Fatalf("claude changed: %+v", obs)
	}
	if link, _ := os.Readlink(filepath.Join(localBin, "claude")); !strings.HasSuffix(link, "/versions/2.1.314") {
		t.Fatalf("link moved to %s", link)
	}
	if _, err := os.Stat(filepath.Join(home, ".cache", "clifleet", "staged", "claude")); !os.IsNotExist(err) {
		t.Fatalf("old stamp kept: %v", err)
	}
}

// Prereleases of one release are different versions: rc.2 → rc.5 installs,
// rc.5 over rc.5 is current, and an older staged rc never replaces a newer one.
func TestNpmApplyComparesPrereleases(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	cases := []struct {
		live, staged string
		want         func(ActionResult) bool
		installs     bool
		stampKept    bool
	}{
		{"0.2.1-rc.2", "0.2.1-rc.5", func(r ActionResult) bool { return r.Skip == "" }, true, true},
		{"0.2.1-rc.5", "0.2.1-rc.5", func(r ActionResult) bool { return r.Skip == "already 0.2.1-rc.5" }, false, true},
		{"0.2.1-rc.5", "0.2.1-rc.2", func(r ActionResult) bool {
			return r.Skip == "installed 0.2.1-rc.5 is newer than staged 0.2.1-rc.2"
		}, false, false},
	}
	for _, c := range cases {
		t.Run(c.live+"_"+c.staged, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "home")
			bin := filepath.Join(root, "bin")
			prefix := filepath.Join(root, "prefix")
			pkgDir := filepath.Join(prefix, "lib", "node_modules", "@deepseek-ai", "dsh")
			cache := filepath.Join(home, ".cache", "clifleet")
			for _, d := range []string{filepath.Join(pkgDir, "lib"), bin, filepath.Join(prefix, "bin"), filepath.Join(cache, "npm", "dsh"), filepath.Join(cache, "staged")} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			writeExe(t, filepath.Join(pkgDir, "lib", "bin.js"), "#!/bin/sh\nprintf '"+c.live+"\\n'\n")
			if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(`{"name":"@deepseek-ai/dsh","version":"`+c.live+`"}`+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("../lib/node_modules/@deepseek-ai/dsh/lib/bin.js", filepath.Join(prefix, "bin", "dsh")); err != nil {
				t.Fatal(err)
			}
			tarball := filepath.Join(cache, "npm", "dsh", "deepseek-ai-dsh-"+c.staged+".tgz")
			writeTarGz(t, tarball, map[string]string{"package/package.json": `{"name":"@deepseek-ai/dsh","version":"` + c.staged + `"}` + "\n"})
			stamp := filepath.Join(cache, "staged", "dsh")
			if err := os.WriteFile(stamp, []byte("npm\t@deepseek-ai/dsh\t"+tarball+"\t"+c.staged+"\t-\t-\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			logPath := filepath.Join(root, "npm.log")
			writeExe(t, filepath.Join(bin, "npm"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$NPM_LOG\"\nexit 1\n")
			script, err := ApplyScript([]Tool{{Name: "dsh", Binary: "dsh"}}, []string{bin, filepath.Join(prefix, "bin")}, []StageStep{{
				Tool: "dsh", Kind: catalog.MethodNpm, Package: "@deepseek-ai/dsh",
			}})
			if err != nil {
				t.Fatal(err)
			}
			out := runSh(t, script, []string{"PATH=" + filepath.Join(prefix, "bin") + ":" + bin + ":/usr/bin:/bin", "HOME=" + home, "NPM_LOG=" + logPath})
			results, _, err := ParseApply(out)
			if err != nil || len(results) != 1 || !c.want(results[0]) {
				t.Fatalf("%v %+v\n%s", err, results, out)
			}
			logged, _ := os.ReadFile(logPath)
			if got := strings.Contains(string(logged), "install"); got != c.installs {
				t.Fatalf("npm install ran=%v, want %v:\n%s", got, c.installs, logged)
			}
			if _, err := os.Stat(stamp); (err == nil) != c.stampKept {
				t.Fatalf("stamp kept=%v, want %v", err == nil, c.stampKept)
			}
		})
	}
}

func TestCursorAgentAliasesShareOneDownload(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(home, 0o755); err != nil || os.MkdirAll(bin, 0o755) != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(root, "agent.tgz")
	writeTarGz(t, pkg, map[string]string{
		"payload/cursor-agent": "#!/bin/sh\nprintf '2026.10.28-abc123\\n'\n",
	})
	install := filepath.Join(root, "install.sh")
	if err := os.WriteFile(install, []byte("DOWNLOAD_URL=\"https://downloads.cursor.com/lab/2026.10.28-abc123/${OS}/${ARCH}/agent-cli-package.tar.gz\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(root, "curl.log")
	writeExe(t, filepath.Join(bin, "curl"), `#!/bin/sh
out=
url=
prev=
for a in "$@"; do
  if [ "$prev" = -o ]; then out=$a; fi
  case $a in http*) url=$a ;; esac
  prev=$a
done
printf '%s\n' "$url" >> "$CURL_LOG"
case $url in
  https://cursor.com/install) cp "$INSTALL_SH" "$out" ;;
  https://downloads.cursor.com/lab/*/agent-cli-package.tar.gz) cp "$AGENT_TGZ" "$out" ;;
  *) printf 'unexpected %s\n' "$url" >&2; exit 1 ;;
esac
`)
	env := []string{
		"PATH=" + bin + ":/usr/bin:/bin",
		"HOME=" + home,
		"INSTALL_SH=" + install,
		"AGENT_TGZ=" + pkg,
		"CURL_LOG=" + log,
	}
	// One script per tool, as fleet runs them.
	for _, step := range []StageStep{
		{Tool: "agent", Kind: catalog.KindRelease, Release: "agent"},
		{Tool: "cursor-agent", Kind: catalog.KindRelease, Release: "cursor_agent"},
	} {
		fetch, err := FetchScript([]string{bin}, []StageStep{step})
		if err != nil {
			t.Fatal(err)
		}
		out := runSh(t, fetch, env)
		results, err := ParseFetch(out)
		if err != nil || len(results) != 1 || results[0].Exit != 0 {
			t.Fatalf("fetch %s %v %+v\n%s", step.Tool, err, results, out)
		}
	}
	got, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(got), "agent-cli-package.tar.gz"); n != 1 {
		t.Fatalf("package downloaded %d times:\n%s", n, got)
	}
	cache := filepath.Join(home, ".cache", "clifleet")
	for _, tool := range []string{"agent", "cursor-agent"} {
		stamp, err := os.ReadFile(filepath.Join(cache, "staged", tool))
		want := filepath.Join(cache, "release", tool, "agent-cli-package.tar.gz")
		if err != nil || !strings.Contains(string(stamp), "\t2026.10.28-abc123\t"+want) {
			t.Fatalf("%s stamp %q %v", tool, stamp, err)
		}
		if _, err := os.Stat(want); err != nil {
			t.Fatalf("%s archive: %v", tool, err)
		}
	}
}

// A live version that cannot be read (keychain prompt, garbage) is no reason
// to refuse: apply goes ahead as it did before the older-stamp check.
func TestApplyGoesAheadWhenLiveVersionIsUnreadable(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	_, localBin, _, env := claudeStampFixture(t, "unreadable", "2.1.314", false)
	got, obs := applyClaude(t, []string{localBin}, env)
	if got.Exit != 0 || got.Skip != "" || got.Blocked != "" {
		t.Fatalf("apply %+v", got)
	}
	if ShortVersion(obs.Version) != "2.1.314" {
		t.Fatalf("claude not applied: %+v", obs)
	}
}

// npm fetch downloads the dependencies too: it installs the packed tarball
// into a throwaway prefix (filling npm's cache) and removes it. A failed
// dependency download is a failed fetch with no stamp, so apply never has to
// reach the registry under its own cap.
func TestNpmFetchWarmsTheCache(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	for _, fail := range []bool{false, true} {
		root := t.TempDir()
		home := filepath.Join(root, "home")
		bin := filepath.Join(root, "bin")
		cache := filepath.Join(root, "xdg")
		if err := os.MkdirAll(bin, 0o755); err != nil || os.MkdirAll(home, 0o755) != nil {
			t.Fatal(err)
		}
		pkg := filepath.Join(root, "dsh.tgz")
		writeTarGz(t, pkg, map[string]string{
			"package/package.json": `{"name":"@deepseek-ai/dsh","version":"0.2.1-rc.5"}`,
		})
		log := filepath.Join(root, "npm.log")
		writeExe(t, filepath.Join(bin, "npm"), `#!/bin/sh
case $1 in
  pack)
    dest=
    prev=
    for a in "$@"; do
      if [ "$prev" = --pack-destination ]; then dest=$a; fi
      prev=$a
    done
    cp "$PKG" "$dest/deepseek-ai-dsh-0.2.1-rc.5.tgz"
    printf 'deepseek-ai-dsh-0.2.1-rc.5.tgz\n'
    ;;
  install)
    prefix=
    prev=
    for a in "$@"; do
      if [ "$prev" = --prefix ]; then prefix=$a; fi
      prev=$a
    done
    printf 'install %s\n' "$*" >> "$NPM_LOG"
    [ -d "$prefix" ] || exit 9
    mkdir -p "$prefix/lib/node_modules/dep"
    [ -z "${NPM_FAIL:-}" ]
    ;;
  *) exit 0 ;;
esac
`)
		env := []string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + home, "XDG_CACHE_HOME=" + cache, "PKG=" + pkg, "NPM_LOG=" + log}
		if fail {
			env = append(env, "NPM_FAIL=1")
		}
		fetch, err := FetchScript([]string{bin}, []StageStep{{Tool: "dsh", Kind: catalog.MethodNpm, Package: "@deepseek-ai/dsh"}})
		if err != nil {
			t.Fatal(err)
		}
		out := runSh(t, fetch, env)
		results, err := ParseFetch(out)
		if err != nil || len(results) != 1 {
			t.Fatalf("fetch %v %+v\n%s", err, results, out)
		}
		got, err := os.ReadFile(log)
		if err != nil {
			t.Fatalf("npm install never ran: %v\n%s", err, out)
		}
		line := string(got)
		if strings.Count(line, "--prefix") != 1 || !strings.Contains(line, "/npm-warm ") ||
			!strings.Contains(line, filepath.Join(cache, "clifleet", "npm", "dsh", "deepseek-ai-dsh-0.2.1-rc.5.tgz")) {
			t.Fatalf("warm install %q", line)
		}
		_, stampErr := os.Stat(filepath.Join(cache, "clifleet", "staged", "dsh"))
		if fail {
			if results[0].Exit != 1 || !strings.Contains(results[0].Detail, "could not download the dependencies") || !os.IsNotExist(stampErr) {
				t.Fatalf("failed warm: %+v stamp %v", results[0], stampErr)
			}
			continue
		}
		if results[0].Exit != 0 || stampErr != nil {
			t.Fatalf("fetch %+v stamp %v\n%s", results[0], stampErr, out)
		}
	}
}
