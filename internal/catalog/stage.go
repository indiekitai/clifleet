package catalog

import (
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

	// ReasonNoStageNative is the fetch/apply status for a self-updater.
	// There is no stable public artifact URL, so the one-shot path stays.
	ReasonNoStageNative = "native updater has no staged artifact; use clifleet upgrade"
	ReasonNoStageBun    = "bun global installs are not staged; use clifleet upgrade"
	ReasonNoStagePnpm   = "pnpm global installs are not staged; use clifleet upgrade"
	ReasonNoStageYarn   = "yarn global installs are not staged; use clifleet upgrade"
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
	Kind      string // "npm" or "brew" when Action is stage
	Package   string
	BrewToken string
	BrewKind  string
	Fetch     *Command
	Apply     *Command
}

// PlanStage chooses a staged upgrade for an observation.
//
// npm downloads a registry tarball with npm pack. Homebrew downloads a
// bottle or cask with brew fetch. Native self-updaters, and bun, pnpm, and
// yarn global installs, are no-stage: clifleet will not invent a download
// URL. A tool that is not installed yields a skip and nil commands.
func PlanStage(spec Spec, obs Observation, methodOverride string) StagePlan {
	if !obs.Present {
		return StagePlan{Action: ActionSkip, Reason: "not installed"}
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
		return StagePlan{Method: MethodNative, Action: ActionNoStage, Reason: ReasonNoStageNative}
	default:
		return StagePlan{Method: cmd.Method, Action: ActionNoStage, Reason: ReasonNoStageNative}
	}
}

func npmStage(tool, pkg string) StagePlan {
	if !safePackage(pkg) {
		return StagePlan{Method: MethodNpm, Action: ActionFail, Reason: "refusing unsafe package \"" + pkg + "\""}
	}
	dest := CacheDisplayRoot + "/npm/" + tool
	fetchArgv := []string{"npm", "pack", "--loglevel=error", "--pack-destination", dest, pkg + "@latest"}
	applyArgv := []string{"npm", "install", "-g", "--no-fund", "--no-audit", dest + "/<staged>.tgz"}
	return StagePlan{
		Method:  MethodNpm,
		Action:  ActionStage,
		Kind:    MethodNpm,
		Package: pkg,
		Fetch: &Command{
			Argv: fetchArgv, Method: MethodNpm, Display: shellquote.Display(fetchArgv), Package: pkg,
		},
		Apply: &Command{
			Argv: applyArgv, Method: MethodNpm, Display: shellquote.Display(applyArgv), Package: pkg,
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
