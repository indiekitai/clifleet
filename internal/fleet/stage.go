package fleet

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/indiekitai/clifleet/internal/catalog"
	"github.com/indiekitai/clifleet/internal/probe"
	"github.com/indiekitai/clifleet/internal/runner"
)

func finishStage(ctx context.Context, result HostResult, target runner.Target, run runner.Runner, opt Options, phase string, names []string, plans []plan, staged []probe.StageStep, tools []probe.Tool, extra []string) HostResult {
	var actions map[string]probe.ActionResult
	var after map[string]catalog.Observation
	if !opt.DryRun && len(staged) > 0 {
		var log string
		actions, after, log = runStageSteps(ctx, target, run, opt, phase, tools, extra, staged)
		if opt.Verbose {
			result.Log = strings.TrimSpace(log)
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

// finishSafe is the default upgrade: fetch every stageable tool, then apply
// the ones that staged. Each tool is its own process and its own timeout, so
// one slow or failed fetch cannot mark its siblings failed.
func finishSafe(ctx context.Context, result HostResult, target runner.Target, run runner.Runner, opt Options, names []string, plans []plan, staged []probe.StageStep, tools []probe.Tool, extra []string) HostResult {
	var fetched, applied map[string]probe.ActionResult
	var after map[string]catalog.Observation
	if !opt.DryRun && len(staged) > 0 {
		var fetchLog, applyLog string
		fetched, _, fetchLog = runStageSteps(ctx, target, run, opt, PhaseFetch, tools, extra, staged)
		var applySteps []probe.StageStep
		for _, step := range staged {
			res, ok := fetched[step.Tool]
			if !ok || res.Blocked != "" || res.Exit != 0 || res.Skip != "" {
				continue
			}
			applySteps = append(applySteps, step)
		}
		if len(applySteps) > 0 {
			applied, after, applyLog = runStageSteps(ctx, target, run, opt, PhaseApply, tools, extra, applySteps)
		}
		if opt.Verbose {
			result.Log = strings.TrimSpace(fetchLog + "\n" + applyLog)
		}
	}
	for i, name := range names {
		result.Rows = append(result.Rows, renderSafeRow(name, plans[i], opt.DryRun, fetched, applied, after))
	}
	if hostBad(result) {
		progress(opt, "✗ %s", result.Name)
	} else {
		progress(opt, "✓ %s", result.Name)
	}
	return result
}

// runStageSteps runs one fetch or apply script per tool. A timeout or a
// non-zero status stays on that tool.
func runStageSteps(ctx context.Context, target runner.Target, run runner.Runner, opt Options, phase string, tools []probe.Tool, extra []string, steps []probe.StageStep) (map[string]probe.ActionResult, map[string]catalog.Observation, string) {
	actions := map[string]probe.ActionResult{}
	after := map[string]catalog.Observation{}
	var logs []string
	for _, step := range steps {
		if ctx.Err() != nil {
			actions[step.Tool] = probe.ActionResult{Tool: step.Tool, Exit: 1, Detail: ctx.Err().Error()}
			continue
		}
		reprobe := reprobeFor(tools, step)
		script, err := stageScript(phase, reprobe, extra, []probe.StageStep{step}, target.Timeout)
		if err != nil {
			actions[step.Tool] = probe.ActionResult{Tool: step.Tool, Exit: 1, Detail: err.Error()}
			continue
		}
		out, stderr, err := run.Run(ctx, target, script)
		if strings.TrimSpace(stderr) != "" {
			logs = append(logs, stderr)
		}
		if err != nil {
			// A host timeout still returns whatever CF1 lines were printed.
			// A tool that already reported its result is not failed with its siblings.
			if res, ok := partialAction(phase, out, step.Tool); ok {
				actions[step.Tool] = res
				continue
			}
			actions[step.Tool] = probe.ActionResult{Tool: step.Tool, Exit: 1, Detail: err.Error()}
			continue
		}
		got, probed, err := parseStage(phase, out)
		if err != nil {
			actions[step.Tool] = probe.ActionResult{Tool: step.Tool, Exit: 1, Detail: err.Error()}
			continue
		}
		res, ok := got[step.Tool]
		if !ok {
			actions[step.Tool] = probe.ActionResult{Tool: step.Tool, Exit: 1, Detail: phase + " did not report a result"}
		} else {
			actions[step.Tool] = res
		}
		for k, v := range probed {
			after[k] = v
		}
	}
	return actions, after, strings.Join(logs, "\n")
}

func reprobeFor(tools []probe.Tool, step probe.StageStep) []probe.Tool {
	for _, t := range tools {
		if t.Name == step.Tool {
			return []probe.Tool{t}
		}
	}
	if step.Tool != "" {
		return []probe.Tool{{Name: step.Tool, Binary: step.Tool}}
	}
	return tools
}

func renderSafeRow(name string, p plan, dry bool, fetched, applied map[string]probe.ActionResult, after map[string]catalog.Observation) Row {
	if p.stage.Action != catalog.ActionStage {
		return renderStageRow(name, p, PhaseFetch, dry, nil, nil)
	}
	if dry {
		row := presentRow(name, p, StDryRun, "", false)
		row.Method = p.stage.Method
		fetchDisp, applyDisp := "", ""
		if p.stage.Fetch != nil {
			fetchDisp = p.stage.Fetch.Display
		}
		if p.stage.Apply != nil {
			applyDisp = p.stage.Apply.Display
		}
		row.Detail = fetchDisp + " → " + applyDisp
		row.Command = row.Detail
		return row
	}
	fr, ok := fetched[name]
	if !ok {
		return presentRow(name, p, StFailed, "fetch did not report a result", true)
	}
	if fr.Blocked != "" {
		row := presentRow(name, p, StBlocked, fr.Blocked, false)
		row.Method = p.stage.Method
		return row
	}
	if fr.Skip != "" {
		return presentRow(name, p, StSkipped, fr.Skip, false)
	}
	if fr.Exit != 0 {
		detail := fr.Detail
		if detail == "" {
			detail = fmt.Sprintf("exit %d", fr.Exit)
		}
		return presentRow(name, p, StFailed, detail, true)
	}
	return renderStageRow(name, p, PhaseApply, false, applied, after)
}

func stageScript(phase string, tools []probe.Tool, extra []string, staged []probe.StageStep, toolTimeout time.Duration) (string, error) {
	if phase == PhaseFetch {
		return probe.FetchScriptBudget(extra, staged, toolTimeout)
	}
	return probe.ApplyScriptBudget(tools, extra, staged, toolTimeout)
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

// partialAction returns the CF1 result a script printed for tool before a host
// error cut it off: the work that line reports already happened on the host.
func partialAction(phase, out, tool string) (probe.ActionResult, bool) {
	out = completeLines(out)
	var results []probe.ActionResult
	if phase == PhaseFetch {
		results, _ = probe.ParseFetch(out)
	} else {
		results, _, _ = probe.ParseApply(out)
	}
	for _, res := range results {
		if res.Tool == tool {
			return res, true
		}
	}
	return probe.ActionResult{}, false
}

// completeLines drops a last line the host error cut off mid-write, so a
// half-printed CF1 line cannot make the parser discard the finished ones.
func completeLines(out string) string {
	return out[:strings.LastIndex(out, "\n")+1]
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
	if res.Blocked != "" {
		row.Status = StBlocked
		row.Detail = res.Blocked
		return row
	}
	if res.Current {
		row.Status = StCurrent
		row.Detail = ""
		return row
	}
	if res.Skip != "" {
		if strings.HasPrefix(res.Skip, "already ") {
			row.Status = StCurrent
			row.Detail = ""
			return row
		}
		if strings.HasPrefix(res.Skip, "needs Node ") {
			row.Status = StBlocked
			row.Detail = res.Skip
			return row
		}
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
