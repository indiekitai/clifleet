package catalog

import (
	"strings"
	"testing"
)

func TestPlanStageNpmAndBrew(t *testing.T) {
	codex := PlanStage(Resolve("codex", nil), withPkg(
		present("codex", "/usr/local/bin/codex", "/usr/local/lib/node_modules/@openai/codex/bin/codex", "0.163.0"),
		"@openai/codex",
	), "")
	if codex.Action != ActionStage || codex.Kind != MethodNpm || codex.Package != "@openai/codex" {
		t.Fatalf("codex npm %+v", codex)
	}
	if codex.Fetch == nil || codex.Apply == nil {
		t.Fatal("codex npm missing commands")
	}
	if !strings.Contains(codex.Fetch.Display, "npm pack") || !strings.Contains(codex.Fetch.Display, "@openai/codex@latest") {
		t.Fatalf("fetch display %q", codex.Fetch.Display)
	}
	if !strings.Contains(codex.Fetch.Display, CacheDisplayRoot+"/npm/codex") {
		t.Fatalf("fetch cache %q", codex.Fetch.Display)
	}
	if !strings.Contains(codex.Apply.Display, "npm install -g --no-fund --no-audit") || !strings.Contains(codex.Apply.Display, "<staged>.tgz") {
		t.Fatalf("apply display %q", codex.Apply.Display)
	}
	if strings.Contains(codex.Apply.Display, "@latest") || strings.Contains(codex.Apply.Display, "npm pack") {
		t.Fatalf("apply should be a local tarball install: %q", codex.Apply.Display)
	}

	cask := PlanStage(Resolve("claude", nil), withBrew(
		present("claude", "/opt/homebrew/bin/claude", "/opt/homebrew/Caskroom/claude-code/2.1.44/claude", "2.1.44"),
		"cask", "claude-code",
	), "")
	if cask.Action != ActionStage || cask.BrewKind != "cask" || cask.BrewToken != "claude-code" {
		t.Fatalf("cask %+v", cask)
	}
	if cask.Fetch.Display != "brew update && brew fetch --cask claude-code" {
		t.Fatalf("cask fetch %q", cask.Fetch.Display)
	}
	if cask.Apply.Display != "brew upgrade --cask claude-code" {
		t.Fatalf("cask apply %q", cask.Apply.Display)
	}

	formula := PlanStage(Resolve("opencode", nil), withBrew(
		present("opencode", "/opt/homebrew/bin/opencode", "/opt/homebrew/Cellar/opencode/1.19.4/bin/opencode", "1.19.4"),
		"formula", "opencode",
	), "")
	if formula.Fetch.Display != "brew update && brew fetch --formula opencode" || formula.Apply.Display != "brew upgrade opencode" {
		t.Fatalf("formula fetch %q apply %q", formula.Fetch.Display, formula.Apply.Display)
	}

	latest := PlanStage(Resolve("claude", nil), withBrew(
		present("claude", "/opt/homebrew/bin/claude", "/opt/homebrew/Caskroom/claude-code@latest/2.1.44/claude", "2.1.44"),
		"cask", "claude-code@latest",
	), "")
	if !strings.Contains(latest.Fetch.Display, "brew fetch --cask claude-code@latest") || !strings.Contains(latest.Apply.Display, "claude-code@latest") {
		t.Fatalf("latest channel %+v", latest.Fetch)
	}
}

func TestPlanStageNoStageAndSkips(t *testing.T) {
	native := PlanStage(Resolve("codex", nil), present("codex", "/home/a/.local/bin/codex", "/home/a/.codex/packages/standalone/current/codex", "0.163.0"), "")
	if native.Action != ActionNoStage || native.Reason != ReasonNoStageNative || native.Fetch != nil || native.Apply != nil {
		t.Fatalf("native %+v", native)
	}
	claude := PlanStage(Resolve("claude", nil), present("claude", "/home/a/.local/bin/claude", "/home/a/.local/bin/claude", "2.1.44"), "")
	if claude.Action != ActionNoStage || claude.Method != MethodNative {
		t.Fatalf("claude native %+v", claude)
	}
	agent := PlanStage(Resolve("agent", nil), present("agent", "/home/a/.local/bin/agent", "/home/a/.local/bin/agent", "2026.10.22"), "")
	if agent.Action != ActionNoStage {
		t.Fatalf("agent %+v", agent)
	}

	cursor := PlanStage(Resolve("cursor", nil), present("cursor", "/usr/local/bin/cursor", "/Applications/Cursor.app/Contents/Resources/app/bin/cursor", "3.0.0"), "")
	if cursor.Action != ActionSkip || cursor.Reason != "no upgrade recipe" || cursor.Fetch != nil {
		t.Fatalf("cursor shim %+v", cursor)
	}

	dsh := PlanStage(Resolve("dsh", nil), present("dsh", "/usr/local/bin/dsh", "/usr/local/bin/dsh", "0.9.1"), "")
	if dsh.Action != ActionSkip || dsh.Reason != "no upgrade recipe" {
		t.Fatalf("dsh native %+v", dsh)
	}
	dshNpm := PlanStage(Resolve("dsh", nil), present("dsh", "/usr/local/bin/dsh", "/usr/local/lib/node_modules/@deepseek-ai/dsh/lib/bin.js", "0.2.1"), "")
	if dshNpm.Action != ActionStage || dshNpm.Package != "@deepseek-ai/dsh" {
		t.Fatalf("dsh npm %+v", dshNpm)
	}

	missing := PlanStage(Resolve("codex", nil), Observation{Tool: "codex", Present: false}, "npm")
	if missing.Action != ActionSkip || missing.Reason != "not installed" || missing.Fetch != nil || missing.Apply != nil {
		t.Fatalf("missing %+v", missing)
	}

	cargo := PlanStage(Resolve("codex", nil), present("codex", "/home/a/.cargo/bin/codex", "/home/a/.cargo/bin/codex", "0.1.0"), "")
	if cargo.Action != ActionSkip || cargo.Reason != "cargo installs are not upgraded automatically" {
		t.Fatalf("cargo %+v", cargo)
	}

	bun := PlanStage(Resolve("codex", nil), withPkg(
		present("codex", "/home/a/.bun/bin/codex", "/home/a/.bun/install/global/node_modules/@openai/codex/bin/codex", "0.1.0"),
		"@openai/codex",
	), "")
	if bun.Action != ActionNoStage || bun.Reason != ReasonNoStageBun || bun.Fetch != nil {
		t.Fatalf("bun %+v", bun)
	}
	pnpm := PlanStage(Resolve("codex", nil), withPkg(
		present("codex", "/home/a/.local/share/pnpm/codex", "/home/a/.local/share/pnpm/global/5/node_modules/@openai/codex/bin/codex", "0.1.0"),
		"@openai/codex",
	), "")
	if pnpm.Action != ActionNoStage || pnpm.Reason != ReasonNoStagePnpm {
		t.Fatalf("pnpm %+v", pnpm)
	}
	yarn := PlanStage(Resolve("codex", nil), withPkg(
		present("codex", "/home/a/.yarn/bin/codex", "/home/a/.yarn/bin/codex", "0.1.0"),
		"@openai/codex",
	), "")
	if yarn.Action != ActionNoStage || yarn.Reason != ReasonNoStageYarn {
		t.Fatalf("yarn %+v", yarn)
	}

	opencode := PlanStage(Resolve("opencode", nil), present("opencode", "/usr/local/bin/opencode", "/usr/local/lib/node_modules/opencode/bin/opencode", "1.0.0"), "")
	if opencode.Action != ActionNoStage || opencode.Method != MethodNative {
		t.Fatalf("opencode npm without a package should stay on the native updater: %+v", opencode)
	}
}

func TestPlanStageRefusesUnsafeTargets(t *testing.T) {
	badBrew := PlanStage(Resolve("claude", nil), withBrew(
		present("claude", "/opt/homebrew/bin/claude", "/opt/homebrew/Caskroom/claude-code/1/claude", "1.0.0"),
		"cask", "claude-code;touch",
	), "")
	if badBrew.Action != ActionFail || badBrew.Fetch != nil || badBrew.Apply != nil {
		t.Fatalf("unsafe brew %+v", badBrew)
	}
	badPkg := PlanStage(Resolve("codex", nil), withPkg(
		present("codex", "/usr/local/bin/codex", "/usr/local/lib/node_modules/codex/bin/codex", "0.1.0"),
		"-rf",
	), "")
	if badPkg.Action != ActionFail || badPkg.Fetch != nil {
		t.Fatalf("dash package %+v", badPkg)
	}
	if !PackageOK("@openai/codex") || PackageOK("-rf") || PackageOK("@scope/-evil") || PackageOK("..") {
		t.Fatal("PackageOK")
	}
	if !BrewTokenOK("claude-code@latest") || BrewTokenOK("claude code") || !BrewKindOK("cask") || BrewKindOK("tap") {
		t.Fatal("brew validators")
	}
}

func TestPlanStageRespectsMethodOverride(t *testing.T) {
	forced := PlanStage(Resolve("claude", nil), present("claude", "/home/a/.local/bin/claude", "/home/a/.local/bin/claude", "2.0.0"), "npm")
	if forced.Action != ActionStage || forced.Package != "@anthropic-ai/claude-code" {
		t.Fatalf("override npm %+v", forced)
	}
	back := PlanStage(Resolve("codex", nil), withPkg(
		present("codex", "/usr/local/bin/codex", "/usr/local/lib/node_modules/@openai/codex/bin/codex", "0.1.0"),
		"@openai/codex",
	), "native")
	if back.Action != ActionNoStage || back.Method != MethodNative {
		t.Fatalf("override native %+v", back)
	}
}

func TestOneShotCommandsUnchanged(t *testing.T) {
	dec := Decide(Resolve("codex", nil), withPkg(
		present("codex", "/usr/local/bin/codex", "/usr/local/lib/node_modules/@openai/codex/bin/codex", "0.163.0"),
		"@openai/codex",
	), "")
	if dec.Command == nil || dec.Command.Package != "@openai/codex" {
		t.Fatalf("%+v", dec)
	}
	if strings.Join(dec.Command.Argv, " ") != "npm install -g --no-fund --no-audit @openai/codex@latest" {
		t.Fatalf("one-shot argv changed: %v", dec.Command.Argv)
	}
	brew := Decide(Resolve("opencode", nil), withBrew(
		present("opencode", "/opt/homebrew/bin/opencode", "/opt/homebrew/Cellar/opencode/1.19.4/bin/opencode", "1.19.4"),
		"formula", "opencode",
	), "")
	if brew.Command == nil || strings.Join(brew.Command.Argv, " ") != "brew upgrade opencode" || brew.Command.BrewKind != "formula" {
		t.Fatalf("one-shot brew %+v", brew.Command)
	}
}
