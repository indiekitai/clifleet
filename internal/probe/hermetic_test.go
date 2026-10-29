package probe

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// The tests run generated scripts with a real sh. On a developer machine the
// production login PATH would reach /opt/homebrew and /usr/local, where a real
// npm or codex can win over the test's fake and an apply can replace it
// (2026-10-29: a run overwrote the real Homebrew codex.js). Keep every
// directory the scripts search inside the test's own HOME.
var prodLoginPathDirs, prodBrewFallbacks []string

func TestMain(m *testing.M) {
	prodLoginPathDirs, prodBrewFallbacks = loginPathDirs, brewFallbacks
	var dirs []string
	for _, d := range loginPathDirs {
		if strings.HasPrefix(d, `"$HOME/`) {
			dirs = append(dirs, d)
		}
	}
	loginPathDirs = dirs
	brewFallbacks = []string{`"$HOME/.linuxbrew/bin/brew"`}
	// Test PATHs end in /usr/bin:/bin for the shell utilities. A real AI CLI
	// there could win over a fake, so refuse to run rather than touch it.
	for _, dir := range []string{"/usr/bin", "/bin"} {
		for _, name := range []string{"codex", "claude", "agent", "cursor-agent", "opencode"} {
			if _, err := os.Lstat(dir + "/" + name); err == nil {
				fmt.Fprintf(os.Stderr, "probe tests: %s/%s exists and could shadow a fake; refusing to run\n", dir, name)
				os.Exit(1)
			}
		}
	}
	os.Exit(m.Run())
}

func TestScriptsStayOffTheRealPrefixes(t *testing.T) {
	steps := []StageStep{{Tool: "codex", Kind: "npm", Package: "@openai/codex"}}
	probe, err := ProbeScript([]Tool{{Name: "codex", Binary: "codex"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	apply, err := ApplyScript([]Tool{{Name: "codex", Binary: "codex"}}, nil, steps)
	if err != nil {
		t.Fatal(err)
	}
	for _, script := range []string{probe, apply} {
		for _, bad := range []string{"/opt/homebrew", "/usr/local", "/home/linuxbrew"} {
			for _, line := range strings.Split(script, "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "#") || !strings.Contains(line, bad) {
					continue
				}
				t.Fatalf("test script still reaches %s: %q", bad, line)
			}
		}
	}
}

// withProductionPaths restores the real login PATH list for a test that only
// inspects script text. Such a test must not run the script.
func withProductionPaths(t *testing.T) {
	t.Helper()
	login, brew := loginPathDirs, brewFallbacks
	loginPathDirs, brewFallbacks = prodLoginPathDirs, prodBrewFallbacks
	t.Cleanup(func() { loginPathDirs, brewFallbacks = login, brew })
}
