package catalog

import (
	"strings"
	"testing"
)

func TestCandidatesHeterogeneous(t *testing.T) {
	global := DefaultTools()
	us := Candidates(global, HostPolicy{Tools: []string{"claude", "codex"}}, nil)
	if strings.Join(us, ",") != "claude,codex" {
		t.Fatalf("us %v", us)
	}
	// --only cannot introduce a regional tool the allowlist excluded.
	if got := Candidates(global, HostPolicy{Tools: []string{"claude", "codex"}}, []string{"dsh", "opencode"}); len(got) != 0 {
		t.Fatalf("only widened the allowlist: %v", got)
	}
	sh := Candidates(global, HostPolicy{Tools: []string{"claude", "opencode", "dsh"}}, nil)
	if strings.Join(sh, ",") != "claude,opencode,dsh" {
		t.Fatalf("shanghai %v", sh)
	}
	ws := Candidates(global, HostPolicy{Skip: []string{"opencode"}}, nil)
	if strings.Join(ws, ",") != "claude,codex" {
		t.Fatalf("workshop %v", ws)
	}
	// skip wins over an allowlist.
	both := Candidates(global, HostPolicy{Tools: []string{"claude", "codex"}, Skip: []string{"claude"}}, nil)
	if strings.Join(both, ",") != "codex" {
		t.Fatalf("skip should win: %v", both)
	}
	only := Candidates(global, HostPolicy{}, []string{"claude"})
	if strings.Join(only, ",") != "claude" {
		t.Fatalf("only %v", only)
	}
}

func TestDecideNeverInstallsMissing(t *testing.T) {
	for _, tool := range []string{"claude", "codex", "opencode", "dsh"} {
		dec := Decide(Resolve(tool, nil), Observation{Tool: tool, Present: false}, "npm")
		if dec.Command != nil {
			t.Fatalf("%s missing produced %v", tool, dec.Command.Argv)
		}
		if dec.Skip != "not installed" {
			t.Fatalf("%s skip %q", tool, dec.Skip)
		}
		if dec.Fail != "" || strings.Contains(dec.Method, "npm") {
			t.Fatalf("%s decision ran a method anyway: %+v", tool, dec)
		}
	}
}

func TestSelectUpgrade(t *testing.T) {
	cases := []struct {
		name     string
		tool     string
		obs      Observation
		override string
		want     []string
		method   string
		fail     bool
		skip     string
	}{
		{
			name:   "claude native",
			tool:   "claude",
			obs:    present("claude", "/home/a/.local/bin/claude", "/home/a/.local/share/claude/versions/2.1.44/claude", "2.1.44"),
			want:   []string{"claude", "update"},
			method: MethodNative,
		},
		{
			name:   "claude brew cask",
			tool:   "claude",
			obs:    withBrew(present("claude", "/opt/homebrew/bin/claude", "/opt/homebrew/Caskroom/claude-code/2.1.44/claude", "2.1.44"), "cask", "claude-code"),
			want:   []string{"brew", "upgrade", "--cask", "claude-code"},
			method: MethodBrew,
		},
		{
			name:   "claude brew latest channel",
			tool:   "claude",
			obs:    withBrew(present("claude", "/opt/homebrew/bin/claude", "/opt/homebrew/Caskroom/claude-code@latest/2.1.44/claude", "2.1.44"), "cask", "claude-code@latest"),
			want:   []string{"brew", "upgrade", "--cask", "claude-code@latest"},
			method: MethodBrew,
		},
		{
			name:   "claude npm",
			tool:   "claude",
			obs:    withPkg(present("claude", "/usr/local/bin/claude", "/usr/local/lib/node_modules/@anthropic-ai/claude-code/cli.js", "2.1.44"), "@anthropic-ai/claude-code"),
			want:   []string{"npm", "install", "-g", "--no-fund", "--no-audit", "@anthropic-ai/claude-code@latest"},
			method: MethodNpm,
		},
		{
			name:   "codex native",
			tool:   "codex",
			obs:    present("codex", "/home/a/.local/bin/codex", "/home/a/.codex/packages/standalone/current/codex", "0.163.0"),
			want:   []string{"codex", "update"},
			method: MethodNative,
		},
		{
			name:   "codex npm",
			tool:   "codex",
			obs:    withPkg(present("codex", "/usr/local/bin/codex", "/usr/local/lib/node_modules/@openai/codex/bin/codex", "0.163.0"), "@openai/codex"),
			want:   []string{"npm", "install", "-g", "--no-fund", "--no-audit", "@openai/codex@latest"},
			method: MethodNpm,
		},
		{
			name:   "codex brew cask",
			tool:   "codex",
			obs:    withBrew(present("codex", "/opt/homebrew/bin/codex", "/opt/homebrew/Caskroom/codex/0.163.0/codex", "0.163.0"), "cask", "codex"),
			want:   []string{"brew", "upgrade", "--cask", "codex"},
			method: MethodBrew,
		},
		{
			name:   "opencode native",
			tool:   "opencode",
			obs:    present("opencode", "/home/a/.local/bin/opencode", "/home/a/.local/share/opencode/opencode", "1.19.4"),
			want:   []string{"opencode", "upgrade"},
			method: MethodNative,
		},
		{
			name:   "opencode brew formula",
			tool:   "opencode",
			obs:    withBrew(present("opencode", "/opt/homebrew/bin/opencode", "/opt/homebrew/Cellar/opencode/1.19.4/bin/opencode", "1.19.4"), "formula", "opencode"),
			want:   []string{"brew", "upgrade", "opencode"},
			method: MethodBrew,
		},
		{
			name:   "opencode npm v1",
			tool:   "opencode",
			obs:    withPkg(present("opencode", "/usr/local/bin/opencode", "/usr/local/lib/node_modules/opencode-ai/bin/opencode", "1.19.4"), "opencode-ai"),
			want:   []string{"npm", "install", "-g", "--no-fund", "--no-audit", "opencode-ai@latest"},
			method: MethodNpm,
		},
		{
			name:   "opencode npm v2",
			tool:   "opencode",
			obs:    withPkg(present("opencode", "/usr/local/bin/opencode", "/usr/local/lib/node_modules/@opencode/cli/bin/opencode", "2.0.0"), "@opencode/cli"),
			want:   []string{"npm", "install", "-g", "--no-fund", "--no-audit", "@opencode/cli@latest"},
			method: MethodNpm,
		},
		{
			name:   "opencode npm without package falls back to opencode upgrade",
			tool:   "opencode",
			obs:    present("opencode", "/usr/local/bin/opencode", "/usr/local/lib/node_modules/opencode/bin/opencode", "1.0.0"),
			want:   []string{"opencode", "upgrade"},
			method: MethodNative,
		},
		{
			name:   "homebrew node_modules is npm not brew",
			tool:   "codex",
			obs:    withPkg(present("codex", "/opt/homebrew/bin/codex", "/opt/homebrew/lib/node_modules/@openai/codex/bin/codex", "0.1.0"), ""),
			want:   []string{"npm", "install", "-g", "--no-fund", "--no-audit", "@openai/codex@latest"},
			method: MethodNpm,
		},
		{
			name:     "override npm",
			tool:     "claude",
			obs:      present("claude", "/home/a/.local/bin/claude", "/home/a/.local/bin/claude", "2.0.0"),
			override: "npm",
			want:     []string{"npm", "install", "-g", "--no-fund", "--no-audit", "@anthropic-ai/claude-code@latest"},
			method:   MethodNpm,
		},
		{
			name: "unsafe brew token",
			tool: "claude",
			obs:  withBrew(present("claude", "/opt/homebrew/bin/claude", "/opt/homebrew/Caskroom/claude-code/1/claude", "1.0.0"), "cask", "claude-code;touch"),
			fail: true,
		},
		{
			name: "cargo is not upgraded",
			tool: "codex",
			obs:  present("codex", "/home/a/.cargo/bin/codex", "/home/a/.cargo/bin/codex", "0.1.0"),
			skip: "cargo installs are not upgraded automatically",
		},
		{
			name: "unknown native tool has no recipe",
			tool: "dsh",
			obs:  present("dsh", "/usr/local/bin/dsh", "/usr/local/bin/dsh", "0.9.1"),
			skip: "no upgrade recipe",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dec := Decide(Resolve(tc.tool, nil), tc.obs, tc.override)
			if tc.fail {
				if dec.Command != nil || dec.Fail == "" {
					t.Fatalf("want fail, got %+v", dec)
				}
				return
			}
			if tc.skip != "" {
				if dec.Command != nil || dec.Skip != tc.skip {
					t.Fatalf("skip got %+v", dec)
				}
				return
			}
			if dec.Command == nil {
				t.Fatalf("no command: %+v", dec)
			}
			if strings.Join(dec.Command.Argv, " ") != strings.Join(tc.want, " ") {
				t.Fatalf("argv %v want %v", dec.Command.Argv, tc.want)
			}
			if dec.Method != tc.method {
				t.Fatalf("method %s want %s", dec.Method, tc.method)
			}
			for _, bad := range []string{"curl", "brew install", "npm install -g " + tc.tool + "\n"} {
				if strings.Contains(dec.Command.Display, "curl") || strings.Contains(dec.Command.Display, "brew install") {
					t.Fatalf("command looks like a fresh install: %s (%s)", dec.Command.Display, bad)
				}
			}
		})
	}
}

func TestRecipeEnablesNativeUpgrade(t *testing.T) {
	spec := Resolve("dsh", []Overlay{{Name: "dsh", Native: []string{"dsh", "update"}}})
	dec := Decide(spec, present("dsh", "/usr/local/bin/dsh", "/usr/local/bin/dsh", "0.9.0"), "")
	if dec.Command == nil || strings.Join(dec.Command.Argv, " ") != "dsh update" {
		t.Fatalf("%+v", dec)
	}
	missing := Decide(spec, Observation{Tool: "dsh", Present: false}, "")
	if missing.Command != nil {
		t.Fatal("recipe installed a missing tool")
	}
}

func present(tool, path, real, version string) Observation {
	return Observation{Tool: tool, Present: true, Path: path, Real: real, Version: version}
}

func withBrew(o Observation, kind, token string) Observation {
	o.BrewKind = kind
	o.BrewToken = token
	return o
}

func withPkg(o Observation, pkg string) Observation {
	o.Package = pkg
	return o
}
