package fleet

import (
	"context"
	"fmt"
	"strings"

	"github.com/indiekitai/clifleet/internal/catalog"
	"github.com/indiekitai/clifleet/internal/probe"
	"github.com/indiekitai/clifleet/internal/runner"
)

func finishStage(ctx context.Context, result HostResult, target runner.Target, run runner.Runner, opt Options, phase string, names []string, plans []plan, staged []probe.StageStep, tools []probe.Tool, extra []string) HostResult {
	var actions map[string]probe.ActionResult
	var after map[string]catalog.Observation
	if !opt.DryRun && len(staged) > 0 {
		script, err := stageScript(phase, tools, extra, staged)
		if err != nil {
			progress(opt, "✗ %s  %s", result.Name, err.Error())
			return hostErrorResult(result, err.Error())
		}
		out, stderr, err := run.Run(ctx, target, script)
		if err != nil {
			progress(opt, "✗ %s  %s", result.Name, err.Error())
			failed := hostErrorResult(result, err.Error())
			for i, name := range names {
				failed.Rows = append(failed.Rows, failedRow(name, plans[i], err.Error()))
			}
			return failed
		}
		actions, after, err = parseStage(phase, out)
		if err != nil {
			progress(opt, "✗ %s  %s", result.Name, err.Error())
			failed := hostErrorResult(result, err.Error())
			for _, name := range names {
				failed.Rows = append(failed.Rows, Row{Tool: name, Status: StFailed, Detail: err.Error(), Failed: true})
			}
			return failed
		}
		if opt.Verbose {
			result.Log = strings.TrimSpace(stderr)
		}
	}

	for i, name := range names {
		result.Rows = append(result.Rows, renderStageRow(name, plans[i], phase, opt.DryRun, actions, after))
	}
	if hostBad(result) {
		progress(opt, "✗ %s", result.Name)
	} else {
		progress(opt, "✓ %s", result.Name)
	}
	return result
}

func stageScript(phase string, tools []probe.Tool, extra []string, staged []probe.StageStep) (string, error) {
	if phase == PhaseFetch {
		return probe.FetchScript(extra, staged)
	}
	return probe.ApplyScript(tools, extra, staged)
}

func parseStage(phase, out string) (map[string]probe.ActionResult, map[string]catalog.Observation, error) {
	var results []probe.ActionResult
	var after map[string]catalog.Observation
	var err error
	if phase == PhaseFetch {
		results, err = probe.ParseFetch(out)
	} else {
		results, after, err = probe.ParseApply(out)
	}
	if err != nil {
		return nil, nil, err
	}
	actions := make(map[string]probe.ActionResult, len(results))
	for _, res := range results {
		actions[res.Tool] = res
	}
	return actions, after, nil
}

func hostErrorResult(base HostResult, msg string) HostResult {
	base.Error = msg
	base.Rows = nil
	base.Log = ""
	return base
}

func renderStageRow(name string, p plan, phase string, dry bool, actions map[string]probe.ActionResult, after map[string]catalog.Observation) Row {
	if strings.HasPrefix(p.dec.Fail, "probe did not") {
		return Row{Tool: name, Status: StFailed, Detail: p.dec.Fail, Failed: true}
	}
	if !p.obs.Present {
		return Row{Tool: name, Status: StMissing, Installed: boolPtr(false)}
	}
	switch p.stage.Action {
	case catalog.ActionFail:
		return presentRow(name, p, StFailed, p.stage.Reason, true)
	case catalog.ActionSkip:
		reason := p.stage.Reason
		if reason == "" || reason == "not installed" {
			reason = "no upgrade recipe"
		}
		return presentRow(name, p, StSkipped, reason, false)
	case catalog.ActionNoStage:
		row := presentRow(name, p, StNoStage, p.stage.Reason, false)
		if p.stage.Method != "" {
			row.Method = p.stage.Method
		}
		return row
	case catalog.ActionStage:
	default:
		return presentRow(name, p, StFailed, "unknown stage action", true)
	}

	cmd := p.stage.Fetch
	if phase == PhaseApply {
		cmd = p.stage.Apply
	}
	row := presentRow(name, p, StInstalled, "", false)
	row.Method = p.stage.Method
	if cmd != nil {
		row.Command = cmd.Display
	}
	if dry {
		row.Status = StDryRun
		if cmd != nil {
			row.Detail = cmd.Display
		}
		return row
	}
	res, ok := actions[name]
	if !ok {
		row.Status = StFailed
		row.Detail = phase + " did not report a result"
		row.Failed = true
		return row
	}
	if res.Skip != "" {
		row.Status = StSkipped
		row.Detail = res.Skip
		return row
	}
	if res.Exit != 0 {
		row.Status = StFailed
		row.Failed = true
		if res.Detail != "" {
			row.Detail = res.Detail
		} else {
			row.Detail = fmt.Sprintf("exit %d", res.Exit)
		}
		return row
	}
	row.Detail = res.Detail
	if phase == PhaseFetch {
		row.Status = StStaged
		return row
	}
	before := row.Version
	if next, ok := after[name]; ok {
		row = noteVersion(row, before, next, StApplied)
		if row.Status == StCurrent {
			row.Detail = ""
		}
		return row
	}
	row.Status = StApplied
	return row
}
