package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/indiekitai/clifleet/internal/config"
)

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
	if code := Execute([]string{"version"}, &out, &errBuf); code != 0 || !strings.Contains(out.String(), Version) {
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
