package probe

import (
	"archive/tar"
	"compress/gzip"
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
	if len(results) != 1 || !results[0].Current || !strings.Contains(results[0].Detail, "up to date") {
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
prev=
for a in "$@"; do
  printf '%s\n' "$a" >> "$CURL_LOG"
  if [ "$prev" = -o ]; then out=$a; fi
  prev=$a
done
cp "$CURL_BODY" "$out"
`)
	writeExe(t, filepath.Join(bin, "codex"), "#!/bin/sh\nprintf 'codex-cli 1.0.0\\n'\n")
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
	if !strings.Contains(string(logged), "https://github.com/openai/codex/releases/latest/download/codex-package-") || !strings.Contains(string(logged), ".tar.gz") {
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
prev=
for a in "$@"; do
  if [ "$prev" = -o ]; then out=$a; fi
  prev=$a
done
cp "$CURL_BODY" "$out"
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
