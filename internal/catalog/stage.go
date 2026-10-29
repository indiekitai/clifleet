package catalog

import (
	"path"
	"strings"

	"github.com/indiekitai/clifleet/internal/shellquote"
)

// Actions for a two-phase upgrade. A missing tool is a skip and never
// produces a fetch or apply command.
const (
	ActionStage   = "stage"
	ActionNoStage = "no-stage"
	ActionSkip    = "skip"
	ActionFail    = "fail"

	// KindRelease is a documented GitHub release asset (Codex package or
	// Claude Code binary). It is not an HTML scrape.
	KindRelease = "release"

	// ReasonNoStageNative is the fetch/apply status for a self-updater that
	// has no documented artifact URL. The one-shot path is explicit.
	ReasonNoStageNative = "native updater has no staged artifact; use clifleet upgrade --one-shot"
	ReasonNoStageBun    = "bun global installs are not staged; use clifleet upgrade --one-shot"
	ReasonNoStagePnpm   = "pnpm global installs are not staged; use clifleet upgrade --one-shot"
	ReasonNoStageYarn   = "yarn global installs are not staged; use clifleet upgrade --one-shot"
)

// CacheDisplayRoot is the per-host cache shown in dry-run lines.
// The remote script uses $XDG_CACHE_HOME when it is set, otherwise ~/.cache.
const CacheDisplayRoot = "${XDG_CACHE_HOME:-$HOME/.cache}/clifleet"

// StagePlan is the fetch/apply decision for one installed tool.
// Fetch and Apply are nil unless Action is ActionStage.
type StagePlan struct {
	Method    string
	Action    string
	Reason    string
	Kind      string // "npm", "brew", or "release" when Action is stage
	Package   string
	BrewToken string
	BrewKind  string
	Release   string // "codex" or "claude" when Kind is release
	Fetch     *Command
	Apply     *Command
}

// PlanStage chooses a staged upgrade for an observation.
//
// npm downloads a registry tarball with npm pack. Homebrew downloads a
// bottle or cask with brew fetch. Native Codex and Claude Code download the
// documented GitHub release asset for the host platform. Other native
// self-updaters, and bun, pnpm, and yarn global installs, are no-stage:
// clifleet will not invent a download URL. A tool that is not installed
// yields a skip and nil commands.
func PlanStage(spec Spec, obs Observation, methodOverride string) StagePlan {
	if !obs.Present {
		return StagePlan{Action: ActionSkip, Reason: "not installed"}
	}
	if name, ok := ManagedBy(obs); ok {
		return StagePlan{Method: name, Action: ActionSkip, Reason: "managed by " + name}
	}
	method := Classify(obs)
	if methodOverride != "" {
		method = methodOverride
	}
	if method == MethodCargo && methodOverride != MethodNative {
		return StagePlan{Method: MethodCargo, Action: ActionSkip, Reason: "cargo installs are not upgraded automatically"}
	}
	cmd, err := commandFor(spec, obs, method)
	if err != nil {
		if strings.HasPrefix(err.Error(), "refusing ") || strings.HasPrefix(err.Error(), "unknown ") {
			return StagePlan{Method: method, Action: ActionFail, Reason: err.Error()}
		}
		return StagePlan{Method: method, Action: ActionSkip, Reason: err.Error()}
	}
	switch cmd.Method {
	case MethodNpm:
		return npmStage(spec.Name, cmd.Package)
	case MethodBrew:
		return brewStage(cmd.BrewToken, cmd.BrewKind)
	case MethodBun:
		return StagePlan{Method: MethodBun, Action: ActionNoStage, Reason: ReasonNoStageBun}
	case MethodPnpm:
		return StagePlan{Method: MethodPnpm, Action: ActionNoStage, Reason: ReasonNoStagePnpm}
	case MethodYarn:
		return StagePlan{Method: MethodYarn, Action: ActionNoStage, Reason: ReasonNoStageYarn}
	case MethodNative:
		if id, ok := nativeRelease(spec.Name, obs.Real); ok {
			return releaseStage(id)
		}
		return StagePlan{Method: MethodNative, Action: ActionNoStage, Reason: ReasonNoStageNative}
	default:
		return StagePlan{Method: cmd.Method, Action: ActionNoStage, Reason: ReasonNoStageNative}
	}
}

// nativeRelease reports the staged-artifact id for a native install.
// cursor-agent's id uses an underscore because it becomes a shell function name.
//
// The name alone is not enough: apply replaces whatever real the probe
// found, so an unrelated binary called "agent", or a wrapper script called
// "claude", would be overwritten. Only an install in the vendor's own
// layout is staged. Anything else is no-stage; --one-shot still runs the
// vendor updater.
//
// Native OpenCode is not staged. Its install script puts the binary in
// ~/.opencode/bin, and the npm channel would add a second copy under
// npm root -g rather than upgrade that one.
func nativeRelease(name, real string) (string, bool) {
	// Remote paths are POSIX; path, not filepath.
	real = path.Clean(real)
	switch name {
	case "codex":
		if strings.Contains(real, "/standalone/") {
			return "codex", true
		}
	case "claude":
		if strings.Contains(real, "/.local/share/claude/versions/") {
			return "claude", true
		}
	case "agent", "cursor-agent":
		if strings.Contains(real, "/.local/share/cursor-agent/versions/") {
			return strings.ReplaceAll(name, "-", "_"), true
		}
	}
	return "", false
}

func releaseStage(id string) StagePlan {
	var fetchDisplay, applyDisplay string
	switch id {
	case "codex":
		// releases.openai.com channel latest, then codex-package-<triple>.tar.gz.
		// GitHub rust-v<version> is the fallback. Apply flips the standalone current link.
		fetchDisplay = "curl -fsSL https://releases.openai.com/codex/releases/<version>/codex-package-<triple>.tar.gz"
		applyDisplay = "swap staged Codex package into place"
	case "claude":
		// downloads.claude.ai publishes manifest.json (sha256 per platform) and
		// the binary at <version>/<platform>/claude. Apply drops it in
		// ~/.local/share/claude/versions/<version> and retargets the symlink.
		fetchDisplay = "curl -fsSL https://downloads.claude.ai/claude-code-releases/<version>/manifest.json && sha256"
		applyDisplay = "retarget ~/.local/bin/claude at the staged version"
	case "agent", "cursor_agent":
		// Official installer publishes downloads.cursor.com/lab/<version>/<os>/<arch>/agent-cli-package.tar.gz
		// and extracts it under ~/.local/share/cursor-agent/versions/<version>.
		fetchDisplay = "curl -fsSL https://downloads.cursor.com/lab/<version>/<os>/<arch>/agent-cli-package.tar.gz"
		applyDisplay = "retarget ~/.local/bin agent symlink at the staged version"
	default:
		return StagePlan{Method: MethodNative, Action: ActionNoStage, Reason: ReasonNoStageNative}
	}
	return StagePlan{
		Method:  MethodNative,
		Action:  ActionStage,
		Kind:    KindRelease,
		Release: id,
		Fetch: &Command{
			Argv: []string{"curl", "-fsSL", fetchDisplay}, Method: MethodNative, Display: fetchDisplay,
		},
		Apply: &Command{
			Argv: []string{"clifleet-apply", id}, Method: MethodNative, Display: applyDisplay,
		},
	}
}

func npmStage(tool, pkg string) StagePlan {
	if !safePackage(pkg) {
		return StagePlan{Method: MethodNpm, Action: ActionFail, Reason: "refusing unsafe package \"" + pkg + "\""}
	}
	dest := CacheDisplayRoot + "/npm/" + tool
	fetchArgv := []string{"npm", "pack", "--loglevel=error", "--pack-destination", dest, pkg + "@latest"}
	applyArgv := []string{"npm", "install", "-g", "--prefix", "<tmp>", "--no-fund", "--no-audit", dest + "/<staged>.tgz"}
	applyDisplay := shellquote.Display(applyArgv) + " && swap the package directory"
	return StagePlan{
		Method:  MethodNpm,
		Action:  ActionStage,
		Kind:    MethodNpm,
		Package: pkg,
		Fetch: &Command{
			Argv: fetchArgv, Method: MethodNpm, Display: shellquote.Display(fetchArgv), Package: pkg,
		},
		Apply: &Command{
			Argv: applyArgv, Method: MethodNpm, Display: applyDisplay, Package: pkg,
		},
	}
}

func brewStage(token, kind string) StagePlan {
	if !BrewTokenOK(token) || !BrewKindOK(kind) {
		return StagePlan{Method: MethodBrew, Action: ActionFail, Reason: "refusing unsafe brew target"}
	}
	var fetchArgv, applyArgv []string
	switch kind {
	case "cask":
		fetchArgv = []string{"brew", "fetch", "--cask", token}
		applyArgv = []string{"brew", "upgrade", "--cask", token}
	default:
		fetchArgv = []string{"brew", "fetch", "--formula", token}
		applyArgv = []string{"brew", "upgrade", token}
	}
	return StagePlan{
		Method:    MethodBrew,
		Action:    ActionStage,
		Kind:      MethodBrew,
		BrewToken: token,
		BrewKind:  kind,
		Fetch: &Command{
			Argv: fetchArgv, Method: MethodBrew,
			Display:   "brew update && " + shellquote.Display(fetchArgv),
			BrewToken: token, BrewKind: kind,
		},
		Apply: &Command{
			Argv: applyArgv, Method: MethodBrew, Display: shellquote.Display(applyArgv),
			BrewToken: token, BrewKind: kind,
		},
	}
}
