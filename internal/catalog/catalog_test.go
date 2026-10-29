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
	var wantSkip []string
	for _, name := range global {
		if name != "opencode" {
			wantSkip = append(wantSkip, name)
		}
	}
	if strings.Join(ws, ",") != strings.Join(wantSkip, ",") {
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

func TestDefaultToolsAndOptIn(t *testing.T) {
	def := map[string]struct{}{}
	for _, name := range DefaultTools() {
		def[name] = struct{}{}
		spec, ok := builtin(name)
		if !ok || spec.Binary == "" {
			t.Fatalf("default %s has no spec", name)
		}
	}
	for _, name := range []string{
		"claude", "codex", "opencode",
		"cursor", "agent", "cursor-agent",
		"gemini", "agy",
		"copilot", "amp", "pi", "aider", "goose",
		"openclaw", "hermes", "kimi", "qwen",
	} {
		if _, ok := def[name]; !ok {
			t.Fatalf("missing default %s", name)
		}
	}
	for _, name := range []string{"dsh", "grok", "kilo", "kiro-cli"} {
		if _, ok := def[name]; ok {
			t.Fatalf("%s must stay opt-in", name)
		}
		spec, ok := builtin(name)
		if !ok || spec.Binary != name {
			t.Fatalf("opt-in %s spec %+v ok=%v", name, spec, ok)
		}
	}
	if _, ok := builtin("minimax"); ok {
		t.Fatal("minimax must stay a user recipe; binary name was not verified")
	}
	// An unknown name still probes. Upgrade stays a skip until a recipe exists.
	spec := Resolve("crush", nil)
	if spec.Binary != "crush" || len(spec.Native) != 0 || spec.Npm != "" {
		t.Fatalf("unknown spec %+v", spec)
	}
	dec := Decide(spec, present("crush", "/usr/local/bin/crush", "/usr/local/bin/crush", "1.0.0"), "")
	if dec.Command != nil || dec.Skip != "no upgrade recipe" {
		t.Fatalf("unknown upgrade %+v", dec)
	}
}

func TestCursorAndAgentAreDistinct(t *testing.T) {
	cursor, ok := builtin("cursor")
	if !ok || cursor.Binary != "cursor" || len(cursor.Native) != 0 || cursor.Brew != "cursor" || cursor.BrewKind != "cask" {
		t.Fatalf("cursor %+v", cursor)
	}
	agent, ok := builtin("agent")
	if !ok || agent.Binary != "agent" || strings.Join(agent.Native, " ") != "agent update" || agent.Brew != "" {
		t.Fatalf("agent %+v", agent)
	}
	legacy, ok := builtin("cursor-agent")
	if !ok || legacy.Binary != "cursor-agent" || strings.Join(legacy.Native, " ") != "cursor-agent update" || legacy.Brew != "cursor-cli" {
		t.Fatalf("cursor-agent %+v", legacy)
	}
}

func TestDecideNeverInstallsMissing(t *testing.T) {
	tools := append(append([]string{}, DefaultTools()...), "dsh", "grok", "kilo", "kiro-cli", "crush")
	for _, tool := range tools {
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
			name: "dsh native has no upgrade subcommand",
			tool: "dsh",
			obs:  present("dsh", "/usr/local/bin/dsh", "/usr/local/bin/dsh", "0.9.1"),
			skip: "no upgrade recipe",
		},
		{
			name:   "dsh npm uses the built-in package",
			tool:   "dsh",
			obs:    present("dsh", "/usr/local/bin/dsh", "/usr/local/lib/node_modules/@deepseek-ai/dsh/lib/bin.js", "0.2.1"),
			want:   []string{"npm", "install", "-g", "--no-fund", "--no-audit", "@deepseek-ai/dsh@latest"},
			method: MethodNpm,
		},
		{
			name:   "cursor editor brew cask",
			tool:   "cursor",
			obs:    withBrew(present("cursor", "/opt/homebrew/bin/cursor", "/opt/homebrew/Caskroom/cursor/3.0.0/Cursor.app/Contents/Resources/app/bin/cursor", "3.0.0"), "cask", "cursor"),
			want:   []string{"brew", "upgrade", "--cask", "cursor"},
			method: MethodBrew,
		},
		{
			name: "cursor editor shim has no native updater",
			tool: "cursor",
			obs:  present("cursor", "/usr/local/bin/cursor", "/Applications/Cursor.app/Contents/Resources/app/bin/cursor", "3.0.0"),
			skip: "no upgrade recipe",
		},
		{
			name:   "agent native",
			tool:   "agent",
			obs:    present("agent", "/home/a/.local/bin/agent", "/home/a/.local/share/cursor-agent/versions/2026.10.22/cursor-agent", "2026.10.22"),
			want:   []string{"agent", "update"},
			method: MethodNative,
		},
		{
			name:   "cursor-agent native",
			tool:   "cursor-agent",
			obs:    present("cursor-agent", "/home/a/.local/bin/cursor-agent", "/home/a/.local/share/cursor-agent/versions/2026.10.22/cursor-agent", "2026.10.22"),
			want:   []string{"cursor-agent", "update"},
			method: MethodNative,
		},
		{
			name:   "cursor-agent brew cask",
			tool:   "cursor-agent",
			obs:    withBrew(present("cursor-agent", "/opt/homebrew/bin/cursor-agent", "/opt/homebrew/Caskroom/cursor-cli/2026.10.22/dist-package/cursor-agent", "2026.10.22"), "cask", "cursor-cli"),
			want:   []string{"brew", "upgrade", "--cask", "cursor-cli"},
			method: MethodBrew,
		},
		{
			name:   "gemini npm",
			tool:   "gemini",
			obs:    present("gemini", "/usr/local/bin/gemini", "/usr/local/lib/node_modules/@google/gemini-cli/bundle/gemini.js", "0.29.2"),
			want:   []string{"npm", "install", "-g", "--no-fund", "--no-audit", "@google/gemini-cli@latest"},
			method: MethodNpm,
		},
		{
			name:   "gemini brew formula",
			tool:   "gemini",
			obs:    withBrew(present("gemini", "/opt/homebrew/bin/gemini", "/opt/homebrew/Cellar/gemini-cli/0.29.2/bin/gemini", "0.29.2"), "formula", "gemini-cli"),
			want:   []string{"brew", "upgrade", "gemini-cli"},
			method: MethodBrew,
		},
		{
			name: "gemini native has no self-update",
			tool: "gemini",
			obs:  present("gemini", "/home/a/.local/bin/gemini", "/home/a/.local/bin/gemini", "0.29.2"),
			skip: "no upgrade recipe",
		},
		{
			name:   "agy native",
			tool:   "agy",
			obs:    present("agy", "/home/a/.local/bin/agy", "/home/a/.local/bin/agy", "1.4.9"),
			want:   []string{"agy", "update"},
			method: MethodNative,
		},
		{
			name:   "copilot native",
			tool:   "copilot",
			obs:    present("copilot", "/home/a/.local/bin/copilot", "/home/a/.local/bin/copilot", "1.0.105"),
			want:   []string{"copilot", "update"},
			method: MethodNative,
		},
		{
			name:   "copilot npm",
			tool:   "copilot",
			obs:    present("copilot", "/usr/local/bin/copilot", "/usr/local/lib/node_modules/@github/copilot/bin/copilot", "1.0.105"),
			want:   []string{"npm", "install", "-g", "--no-fund", "--no-audit", "@github/copilot@latest"},
			method: MethodNpm,
		},
		{
			name:   "copilot brew cask",
			tool:   "copilot",
			obs:    withBrew(present("copilot", "/opt/homebrew/bin/copilot", "/opt/homebrew/Caskroom/copilot-cli/1.0.105/copilot", "1.0.105"), "cask", "copilot-cli"),
			want:   []string{"brew", "upgrade", "--cask", "copilot-cli"},
			method: MethodBrew,
		},
		{
			name:   "amp native",
			tool:   "amp",
			obs:    present("amp", "/home/a/.local/bin/amp", "/home/a/.local/bin/amp", "1.0.0"),
			want:   []string{"amp", "update"},
			method: MethodNative,
		},
		{
			name:   "amp old npm package from path",
			tool:   "amp",
			obs:    present("amp", "/usr/local/bin/amp", "/usr/local/lib/node_modules/@sourcegraph/amp/bin/amp", "0.1.0"),
			want:   []string{"npm", "install", "-g", "--no-fund", "--no-audit", "@sourcegraph/amp@latest"},
			method: MethodNpm,
		},
		{
			name:   "pi native updates the cli only",
			tool:   "pi",
			obs:    present("pi", "/home/a/.local/bin/pi", "/home/a/.local/bin/pi", "1.4.0"),
			want:   []string{"pi", "update"},
			method: MethodNative,
		},
		{
			name:   "pi npm",
			tool:   "pi",
			obs:    present("pi", "/usr/local/bin/pi", "/usr/local/lib/node_modules/@earendil-works/pi-coding-agent/dist/pi", "1.4.0"),
			want:   []string{"npm", "install", "-g", "--no-fund", "--no-audit", "@earendil-works/pi-coding-agent@latest"},
			method: MethodNpm,
		},
		{
			name:   "aider native",
			tool:   "aider",
			obs:    present("aider", "/home/a/.local/bin/aider", "/home/a/.local/bin/aider", "0.86.2"),
			want:   []string{"aider", "--upgrade", "--yes-always"},
			method: MethodNative,
		},
		{
			name:   "aider brew formula",
			tool:   "aider",
			obs:    withBrew(present("aider", "/opt/homebrew/bin/aider", "/opt/homebrew/Cellar/aider/0.86.2/bin/aider", "0.86.2"), "formula", "aider"),
			want:   []string{"brew", "upgrade", "aider"},
			method: MethodBrew,
		},
		{
			name:   "goose native",
			tool:   "goose",
			obs:    present("goose", "/home/a/.local/bin/goose", "/home/a/.local/bin/goose", "1.56.0"),
			want:   []string{"goose", "update"},
			method: MethodNative,
		},
		{
			name:   "goose brew formula is block-goose-cli",
			tool:   "goose",
			obs:    withBrew(present("goose", "/opt/homebrew/bin/goose", "/opt/homebrew/Cellar/block-goose-cli/1.56.0/bin/goose", "1.56.0"), "formula", "block-goose-cli"),
			want:   []string{"brew", "upgrade", "block-goose-cli"},
			method: MethodBrew,
		},
		{
			name:   "openclaw npm path uses the native updater",
			tool:   "openclaw",
			obs:    present("openclaw", "/usr/local/bin/openclaw", "/usr/local/lib/node_modules/openclaw/dist/index.js", "2026.10.1"),
			want:   []string{"openclaw", "update", "--yes"},
			method: MethodNative,
		},
		{
			name:   "hermes native",
			tool:   "hermes",
			obs:    present("hermes", "/home/a/.local/bin/hermes", "/home/a/.hermes/hermes", "0.16.0"),
			want:   []string{"hermes", "update"},
			method: MethodNative,
		},
		{
			name:   "kimi native",
			tool:   "kimi",
			obs:    present("kimi", "/home/a/.local/bin/kimi", "/home/a/.local/bin/kimi", "0.43.0"),
			want:   []string{"kimi", "upgrade", "--yes"},
			method: MethodNative,
		},
		{
			name:   "kimi npm",
			tool:   "kimi",
			obs:    present("kimi", "/usr/local/bin/kimi", "/usr/local/lib/node_modules/@moonshot-ai/kimi-code/bin/kimi", "0.43.0"),
			want:   []string{"npm", "install", "-g", "--no-fund", "--no-audit", "@moonshot-ai/kimi-code@latest"},
			method: MethodNpm,
		},
		{
			name:   "qwen native",
			tool:   "qwen",
			obs:    present("qwen", "/home/a/.local/bin/qwen", "/home/a/.local/bin/qwen", "0.20.0"),
			want:   []string{"qwen", "update"},
			method: MethodNative,
		},
		{
			name:   "qwen npm",
			tool:   "qwen",
			obs:    present("qwen", "/usr/local/bin/qwen", "/usr/local/lib/node_modules/@qwen-code/qwen-code/bundle/qwen.js", "0.20.0"),
			want:   []string{"npm", "install", "-g", "--no-fund", "--no-audit", "@qwen-code/qwen-code@latest"},
			method: MethodNpm,
		},
		{
			name:   "grok opt-in native",
			tool:   "grok",
			obs:    present("grok", "/home/a/.local/bin/grok", "/home/a/.local/bin/grok", "1.0.0"),
			want:   []string{"grok", "update"},
			method: MethodNative,
		},
		{
			name:   "kilo opt-in native",
			tool:   "kilo",
			obs:    present("kilo", "/home/a/.local/bin/kilo", "/home/a/.local/bin/kilo", "1.0.0"),
			want:   []string{"kilo", "upgrade"},
			method: MethodNative,
		},
		{
			name:   "kiro-cli opt-in native",
			tool:   "kiro-cli",
			obs:    present("kiro-cli", "/home/a/.local/bin/kiro-cli", "/home/a/.local/bin/kiro-cli", "1.0.0"),
			want:   []string{"kiro-cli", "update", "--non-interactive"},
			method: MethodNative,
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

func TestManagedByShimIsNotUpgraded(t *testing.T) {
	obs := present("codex", "/home/a/.local/share/mise/shims/codex", "/usr/bin/mise", "0.99.0")
	name, ok := ManagedBy(obs)
	if !ok || name != "mise" {
		t.Fatalf("ManagedBy %+v %q %v", obs, name, ok)
	}
	dec := Decide(Resolve("codex", nil), obs, "native")
	if dec.Command != nil || dec.Skip != "managed by mise" || dec.Method != "mise" {
		t.Fatalf("override must not fight mise: %+v", dec)
	}
	plan := PlanStage(Resolve("codex", nil), obs, "")
	if plan.Action != ActionSkip || plan.Reason != "managed by mise" || plan.Fetch != nil || plan.Apply != nil {
		t.Fatalf("plan %+v", plan)
	}
	asdf := present("claude", "/home/a/.asdf/shims/claude", "/home/a/.asdf/shims/claude", "2.0.0")
	dec = Decide(Resolve("claude", nil), asdf, "")
	if dec.Command != nil || dec.Skip != "managed by asdf" {
		t.Fatalf("asdf %+v", dec)
	}
	plain := present("codex", "/home/a/.local/bin/codex", "/home/a/.codex/packages/standalone/current/codex", "0.1.0")
	if _, ok := ManagedBy(plain); ok {
		t.Fatal("standalone codex looked managed")
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
