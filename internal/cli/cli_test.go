package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/indiekitai/clifleet/internal/config"
	"github.com/indiekitai/clifleet/internal/selfupdate"
)

var testCache string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "clifleet-cache")
	if err != nil {
		panic(err)
	}
	testCache = dir
	os.Setenv("XDG_CACHE_HOME", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestStatusDryRun(t *testing.T) {
	path := writeExample(t)
	var out, errBuf bytes.Buffer
	code := Execute([]string{"status", "--dry-run", "--config", path}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out.String(), errBuf.String())
	}
	text := out.String()
	if !strings.Contains(text, "no connections opened") {
		t.Fatal(text)
	}
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[0] == "us-server" && (strings.Contains(line, "opencode") || strings.Contains(line, "dsh")) {
			t.Fatalf("us-server line leaked: %s", line)
		}
		if fields[0] == "workshop" && strings.Contains(line, "opencode") {
			t.Fatalf("workshop line leaked: %s", line)
		}
	}
	if !strings.Contains(text, "shanghai") || !strings.Contains(text, "dsh") {
		t.Fatalf("expected shanghai/dsh in\n%s", text)
	}
	raw, err := os.ReadFile(filepath.Join(testCache, "clifleet", "last-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"command": "status"`) || !strings.Contains(string(raw), `"dry_run": true`) {
		t.Fatalf("last report:\n%s", raw)
	}
}

func TestOnlyCannotWidenAllowlist(t *testing.T) {
	path := writeExample(t)
	var out, errBuf bytes.Buffer
	code := Execute([]string{"status", "--dry-run", "--config", path, "--only", "dsh"}, &out, &errBuf)
	if code != 0 {
		t.Fatal(out.String(), errBuf.String())
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.Contains(line, "us-server") && strings.Contains(line, "dsh") {
			t.Fatalf("--only widened us-server: %s", line)
		}
	}
	if !strings.Contains(out.String(), "shanghai") || !strings.Contains(out.String(), "dsh") {
		t.Fatalf("shanghai should still probe dsh\n%s", out.String())
	}
}

func TestInitAndDoctorOffline(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	var out, errBuf bytes.Buffer
	if code := Execute([]string{"init"}, &out, &errBuf); code != 0 {
		t.Fatal(code, errBuf.String())
	}
	path := filepath.Join(home, "clifleet", "hosts.toml")
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errBuf.Reset()
	if code := Execute([]string{"init"}, &out, &errBuf); code != 1 {
		t.Fatalf("second init exit %d", code)
	}
	out.Reset()
	errBuf.Reset()
	code := Execute([]string{"doctor", "--offline"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("doctor exit %d\n%s\n%s", code, out.String(), errBuf.String())
	}
	if !strings.Contains(out.String(), "local") || !strings.Contains(out.String(), "not connected") {
		t.Fatal(out.String())
	}
}

func TestHelpAndVersion(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := Execute(nil, &out, &errBuf); code != 0 || !strings.Contains(out.String(), "Upgrade your AI CLIs across every machine") {
		t.Fatalf("root help %d %s", code, out.String())
	}
	out.Reset()
	if code := Execute([]string{"version"}, &out, &errBuf); code != 0 || out.String() != "clifleet "+version()+"\n" || version() == "" {
		t.Fatal(out.String())
	}
	errBuf.Reset()
	if code := Execute([]string{"upgrade", "--help"}, &out, &errBuf); code != 0 {
		t.Fatal(code)
	}
	help := errBuf.String()
	if !strings.Contains(help, "claude update") || !strings.Contains(help, "not already") {
		t.Fatal(help)
	}
	out.Reset()
	errBuf.Reset()
	if code := Execute([]string{"nope"}, &out, &errBuf); code != 1 {
		t.Fatal(code)
	}
}

func TestFetchApplyFlags(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := Execute([]string{"upgrade", "--help"}, &out, &errBuf); code != 0 {
		t.Fatal(code)
	}
	help := errBuf.String()
	for _, want := range []string{"--fetch", "--apply", "--one-shot", "no-stage", "npm pack", "--hosts", "--only", "--timeout", "--parallel", "--json", "--dry-run"} {
		if !strings.Contains(help, want) {
			t.Fatalf("help missing %q\n%s", want, help)
		}
	}
	errBuf.Reset()
	if code := Execute([]string{"upgrade", "--fetch", "--apply"}, &out, &errBuf); code != 1 || !strings.Contains(errBuf.String(), "separate steps") {
		t.Fatalf("both flags %d %s", code, errBuf.String())
	}
	errBuf.Reset()
	if code := Execute([]string{"fetch", "--apply"}, &out, &errBuf); code != 1 || !strings.Contains(errBuf.String(), "separate steps") {
		t.Fatalf("alias %d %s", code, errBuf.String())
	}

	path := filepath.Join(t.TempDir(), "hosts.toml")
	body := "[[hosts]]\nname = \"here\"\nlocal = true\ntools = [\"clifleet-no-such-tool\"]\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	args := []string{"--config", path, "--hosts", "here", "--only", "clifleet-no-such-tool", "--timeout", "30s", "--parallel", "1"}

	out.Reset()
	errBuf.Reset()
	code := Execute(append([]string{"upgrade", "--dry-run"}, args...), &out, &errBuf)
	if code != 0 {
		t.Fatalf("safe upgrade %d\n%s\n%s", code, out.String(), errBuf.String())
	}
	if strings.Contains(errBuf.String(), noStageTip) {
		t.Fatalf("missing tool should not warn about no-stage\n%s", errBuf.String())
	}
	if !strings.Contains(out.String(), "not installed") {
		t.Fatalf("one-shot output\n%s", out.String())
	}

	out.Reset()
	errBuf.Reset()
	code = Execute(append([]string{"upgrade", "--fetch", "--dry-run", "--json"}, args...), &out, &errBuf)
	if code != 0 {
		t.Fatalf("fetch json %d\n%s\n%s", code, out.String(), errBuf.String())
	}
	if !strings.Contains(out.String(), `"phase": "fetch"`) || strings.Contains(errBuf.String(), noStageTip) {
		t.Fatalf("stdout %s\nstderr %s", out.String(), errBuf.String())
	}

	out.Reset()
	errBuf.Reset()
	code = Execute(append([]string{"fetch", "--dry-run"}, args...), &out, &errBuf)
	if code != 0 || !strings.Contains(out.String(), "not installed") || !strings.Contains(out.String(), "would stage") {
		t.Fatalf("fetch alias %d\n%s\n%s", code, out.String(), errBuf.String())
	}
	if strings.Contains(errBuf.String(), noStageTip) {
		t.Fatal("fetch printed the no-stage tip")
	}

	out.Reset()
	errBuf.Reset()
	code = Execute(append([]string{"apply", "--dry-run", "--json"}, args...), &out, &errBuf)
	if code != 0 || !strings.Contains(out.String(), `"phase": "apply"`) {
		t.Fatalf("apply %d\n%s\n%s", code, out.String(), errBuf.String())
	}
}

func TestMissingConfig(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := Execute([]string{"status", "--config", filepath.Join(t.TempDir(), "missing.toml")}, &out, &errBuf)
	if code != 1 || !strings.Contains(errBuf.String(), "clifleet init") {
		t.Fatalf("%d %s", code, errBuf.String())
	}
}

func writeExample(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hosts.toml")
	if err := os.WriteFile(path, []byte(config.Example), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// ssh that connects and then hangs must not stall doctor past its budget.
func TestDoctorTimesOutAHungHost(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\nif [ \"$1\" = -V ]; then echo OpenSSH_test >&2; exit 0; fi\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	fleet := filepath.Join(dir, "hosts.toml")
	if err := os.WriteFile(fleet, []byte("[[hosts]]\nname = \"hung\"\nssh = \"hung\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := doctorGrace
	doctorGrace = 0
	t.Cleanup(func() { doctorGrace = old })
	var out, errBuf bytes.Buffer
	start := time.Now()
	code := Execute([]string{"doctor", "--config", fleet, "--timeout", "1s"}, &out, &errBuf)
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("doctor took %s", took)
	}
	if code != 2 || !strings.Contains(out.String(), "timed out after 1s") {
		t.Fatalf("exit %d\n%s\n%s", code, out.String(), errBuf.String())
	}
}

func TestSelfUpdateChecksAndLeavesASourceBuild(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/releases/latest" {
			http.Redirect(w, req, "/releases/tag/v9.9.9", http.StatusFound)
			return
		}
		http.NotFound(w, req)
	}))
	defer srv.Close()
	oldURL, oldPath, oldVersion := selfupdate.BaseURL, selfPath, Version
	defer func() { selfupdate.BaseURL, selfPath, Version = oldURL, oldPath, oldVersion }()
	selfupdate.BaseURL = srv.URL + "/releases"
	target := filepath.Join(t.TempDir(), "clifleet")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	selfPath = func() (string, error) { return target, nil }

	Version = "dev"
	var out, errBuf bytes.Buffer
	if code := Execute([]string{"update", "--check"}, &out, &errBuf); code != 0 || !strings.Contains(out.String(), "latest release is v9.9.9") {
		t.Fatalf("check %d %s %s", code, out.String(), errBuf.String())
	}
	out.Reset()
	errBuf.Reset()
	if code := Execute([]string{"update"}, &out, &errBuf); code != 2 || !strings.Contains(errBuf.String(), "--force") {
		t.Fatalf("source build %d %s %s", code, out.String(), errBuf.String())
	}
	out.Reset()
	Version = "v9.9.9"
	if code := Execute([]string{"self-update"}, &out, &errBuf); code != 0 || !strings.Contains(out.String(), "is current") {
		t.Fatalf("current %d %s", code, out.String())
	}
	if got, _ := os.ReadFile(target); string(got) != "old" {
		t.Fatalf("replaced: %q", got)
	}
}
