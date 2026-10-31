package fleet

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/indiekitai/clifleet/internal/catalog"
	"github.com/indiekitai/clifleet/internal/probe"
	"github.com/indiekitai/clifleet/internal/runner"
	"github.com/indiekitai/clifleet/internal/semver"
)

func finishStage(ctx context.Context, result HostResult, target runner.Target, run runner.Runner, opt Options, phase string, names []string, plans []plan, staged []probe.StageStep, tools []probe.Tool, extra []string) HostResult {
	var actions map[string]probe.ActionResult
	var after map[string]catalog.Observation
	if !opt.DryRun && len(staged) > 0 {
		var log, stopped string
		actions, after, log, stopped = runStageSteps(ctx, target, run, opt, phase, tools, extra, staged)
		result.Error = stopped
		if opt.Verbose {
			result.Log = strings.TrimSpace(log)
		}
	}

	for i, name := range names {
		result.Rows = append(result.Rows, renderStageRow(name, plans[i], phase, opt.DryRun, actions, after))
	}
	hostDone(opt, result.Name, hostBad(result), "")
	return result
}

// finishSafe is the default upgrade: fetch every stageable tool, then apply
// the ones that staged. Each tool is its own process and its own timeout, so
// one slow or failed fetch cannot mark its siblings failed.
func finishSafe(ctx context.Context, result HostResult, target runner.Target, run runner.Runner, opt Options, names []string, plans []plan, staged []probe.StageStep, tools []probe.Tool, extra []string) HostResult {
	var fetched, applied map[string]probe.ActionResult
	var after map[string]catalog.Observation
	if !opt.DryRun && len(staged) > 0 {
		var fetchLog, applyLog, stopped string
		fetched, _, fetchLog, stopped = runStageSteps(ctx, target, run, opt, PhaseFetch, tools, extra, staged)
		result.Error = stopped
		var applySteps []probe.StageStep
		for _, step := range staged {
			res, ok := fetched[step.Tool]
			if !ok || res.Blocked != "" || res.Exit != 0 || res.Skip != "" {
				continue
			}
			applySteps = append(applySteps, step)
		}
		if len(applySteps) > 0 {
			applied, after, applyLog, stopped = runStageSteps(ctx, target, run, opt, PhaseApply, tools, extra, applySteps)
			if result.Error == "" {
				result.Error = stopped
			}
		}
		if opt.Verbose {
			result.Log = strings.TrimSpace(fetchLog + "\n" + applyLog)
		}
	}
	for i, name := range names {
		result.Rows = append(result.Rows, renderSafeRow(name, plans[i], opt.DryRun, fetched, applied, after))
	}
	hostDone(opt, result.Name, hostBad(result), "")
	return result
}

// runStageSteps runs one fetch or apply script per tool. A timeout or a
// non-zero status stays on that tool. An interrupt also comes back as
// stopped, so a run cut short by Ctrl-C never reports success, even when
// every tool it reached had already printed its result.
func runStageSteps(ctx context.Context, target runner.Target, run runner.Runner, opt Options, phase string, tools []probe.Tool, extra []string, steps []probe.StageStep) (actions map[string]probe.ActionResult, after map[string]catalog.Observation, log, stopped string) {
	actions = map[string]probe.ActionResult{}
	after = map[string]catalog.Observation{}
	var logs []string
	for _, step := range steps {
		if ctx.Err() != nil {
			actions[step.Tool] = probe.ActionResult{Tool: step.Tool, Exit: 1, Detail: ctx.Err().Error()}
			if stopped == "" {
				stopped = runner.ErrInterrupted.Error()
			}
			continue
		}
		stageStart(opt, target.Name, phase, step.Tool)
		started := time.Now()
		func() {
			reprobe := reprobeFor(tools, step)
			script, err := stageScript(phase, reprobe, extra, []probe.StageStep{step}, target.Timeout)
			if err != nil {
				actions[step.Tool] = probe.ActionResult{Tool: step.Tool, Exit: 1, Detail: err.Error()}
				return
			}
			out, stderr, err := run.Run(ctx, target, script)
			if strings.TrimSpace(stderr) != "" {
				logs = append(logs, stderr)
			}
			if err != nil {
				if errors.Is(err, runner.ErrInterrupted) && stopped == "" {
					stopped = err.Error()
				}
				// A host timeout still returns whatever CF1 lines were printed.
				// A tool that already reported its result is not failed with its siblings.
				if res, ok := partialAction(phase, out, step.Tool); ok {
					actions[step.Tool] = res
					return
				}
				actions[step.Tool] = probe.ActionResult{Tool: step.Tool, Exit: 1, Detail: err.Error()}
				return
			}
			got, probed, err := parseStage(phase, out)
			if err != nil {
				actions[step.Tool] = probe.ActionResult{Tool: step.Tool, Exit: 1, Detail: err.Error()}
				return
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
		}()
		stageEnd(opt, target.Name, phase, step.Tool, actions[step.Tool], time.Since(started))
	}
	return actions, after, strings.Join(logs, "\n"), stopped
}

// stageStart and stageEnd report one tool step on a host, so a long fetch
// does not look hung. Plain lines only.
func stageStart(opt Options, host, phase, tool string) {
	verb := "fetching…"
	if phase == PhaseApply {
		verb = "applying…"
	}
	progress(opt, "  %s %s: %s", host, tool, verb)
}

func stageEnd(opt Options, host, phase, tool string, res probe.ActionResult, took time.Duration) {
	reason := func(s string) string {
		s = firstLine(s)
		if r := []rune(s); len(r) > 80 {
			s = string(r[:80]) + "…"
		}
		return s
	}
	var out string
	switch {
	case res.Blocked != "":
		out = "blocked (" + reason(res.Blocked) + ")"
	case res.Skip != "" && strings.HasPrefix(res.Skip, "already "):
		out = "up to date"
	case res.Skip != "":
		out = "skipped (" + reason(res.Skip) + ")"
	case res.Exit != 0:
		d := reason(res.Detail)
		if d == "" {
			d = fmt.Sprintf("exit %d", res.Exit)
		}
		out = "failed (" + d + ")"
	case res.Current:
		out = "up to date"
	case phase == PhaseApply:
		out = "applied"
	default:
		out = "staged (" + took.Round(time.Second).String() + ")"
	}
	progress(opt, "  %s %s: %s", host, tool, out)
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
	if p.current {
		return currentRow(name, p)
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
	if p.current {
		return currentRow(name, p)
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
		if phase == PhaseApply {
			return applyDryRow(row, p, cmd)
		}
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
	if moved, ok := brewUpgradedAnyway(phase, p, row.Version, res, after[name]); ok {
		return moved
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
		row.StagedVersion = probe.ShortVersion(res.Version)
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

// applyDryRow is an --apply dry run of one tool, read from the stamp the
// probe saw: nothing staged and an older stamp are skips as in the real run,
// a blocked stamp stays blocked, and the rest would apply.
func applyDryRow(row Row, p plan, cmd *catalog.Command) Row {
	obs := p.obs
	switch {
	case !obs.Staged:
		row.Status = StSkipped
		row.Detail = "nothing staged"
		return row
	case obs.StagedBlocked != "":
		row.Status = StBlocked
		row.Detail = obs.StagedBlocked
		return row
	}
	row.StagedVersion = obs.StagedVersion
	if obs.StagedVersion != "" && !unreadableVersion(obs.Version) {
		live := probe.ShortVersion(obs.Version)
		if c, ok := semver.Compare(live, obs.StagedVersion); ok && c >= 0 {
			if c == 0 {
				row.Status = StCurrent
				return row
			}
			row.Status = StSkipped
			row.Detail = "installed " + live + " is newer than staged " + obs.StagedVersion
			return row
		}
	}
	row.Status = StDryRun
	if cmd != nil {
		row.Detail = cmd.Display
	}
	return row
}

// brewUpgradedAnyway covers brew upgrade pouring and linking the new keg and
// then exiting non-zero (an Xcode check, a dependent, cleanup): the reprobe
// shows the new version, so the row says applied and keeps brew's complaint.
// Only brew: a failed npm or release apply rolls back, and a version that
// moved after a failed rollback must stay a failure.
func brewUpgradedAnyway(phase string, p plan, before string, res probe.ActionResult, next catalog.Observation) (Row, bool) {
	if phase != PhaseApply || p.stage.Kind != catalog.MethodBrew || (res.Blocked == "" && res.Exit == 0) {
		return Row{}, false
	}
	if before == "unknown" || unreadableVersion(p.obs.Version) || !next.Present || unreadableVersion(next.Version) {
		return Row{}, false
	}
	afterV := probe.ShortVersion(next.Version)
	if afterV == "" || afterV == before {
		return Row{}, false
	}
	complaint := res.Blocked
	if complaint == "" {
		complaint = res.Detail
	}
	if complaint == "" {
		complaint = fmt.Sprintf("exit %d", res.Exit)
	}
	row := presentRow(p.spec.Name, p, StInstalled, "brew upgrade also reported: "+complaint, false)
	row.Method = p.stage.Method
	if p.stage.Apply != nil {
		row.Command = p.stage.Apply.Display
	}
	return noteVersion(row, before, next, StApplied), true
}

// currentRow is a tool fetch skipped: its installed version already is the
// newest one the control machine found on its channel.
func currentRow(name string, p plan) Row {
	row := presentRow(name, p, StCurrent, "", false)
	row.Method = p.stage.Method
	return row
}
