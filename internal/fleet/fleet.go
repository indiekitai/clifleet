// Package fleet fans a status or upgrade out across hosts.
package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/indiekitai/clifleet/internal/catalog"
	"github.com/indiekitai/clifleet/internal/config"
	"github.com/indiekitai/clifleet/internal/probe"
	"github.com/indiekitai/clifleet/internal/runner"
)

const (
	StInstalled   = "installed"
	StMissing     = "not installed"
	StWouldProbe  = "would probe"
	StUnreachable = "unreachable"
	StUpgraded    = "upgraded"
	StCurrent     = "up to date"
	StSkipped     = "skipped"
	StFailed      = "failed"
	StDryRun      = "dry-run"
	StStaged      = "staged"
	StNoStage     = "no-stage"
	StApplied     = "applied"
)

// StatusBudget is the per-host cap for status when the fleet file does not
// set timeout and the CLI does not pass --timeout. A probe should not sit
// for the upgrade budget. Upgrade, fetch, and apply keep DefaultTimeout.
const StatusBudget = 45 * time.Second

// Phases of clifleet upgrade. The default is one shot. Fetch only downloads.
// Apply replaces binaries from the cache fetch wrote.
const (
	PhaseUpgrade = "upgrade"
	PhaseFetch   = "fetch"
	PhaseApply   = "apply"
)

// Options controls one status or upgrade run.
type Options struct {
	Hosts      []string
	Only       []string
	DryRun     bool
	Parallel   int
	Timeout    time.Duration
	Verbose    bool
	Phase      string
	OnProgress func(string)
}

func (o Options) phase() string {
	switch o.Phase {
	case PhaseFetch, PhaseApply:
		return o.Phase
	default:
		return PhaseUpgrade
	}
}

// Row is one cell of the host × tool matrix.
type Row struct {
	Tool       string `json:"name"`
	Version    string `json:"version,omitempty"`
	VersionRaw string `json:"version_raw,omitempty"`
	Method     string `json:"method,omitempty"`
	Status     string `json:"status"`
	Detail     string `json:"detail,omitempty"`
	Path       string `json:"path,omitempty"`
	Command    string `json:"command,omitempty"`
	Installed  *bool  `json:"installed,omitempty"`
	Failed     bool   `json:"-"`
}

// HostResult is one machine.
type HostResult struct {
	Name        string `json:"name"`
	Destination string `json:"destination"`
	Local       bool   `json:"local"`
	Error       string `json:"error,omitempty"`
	Log         string `json:"-"`
	Rows        []Row  `json:"tools"`
}

// Report is the whole fleet run.
type Report struct {
	Command string       `json:"command"`
	Phase   string       `json:"phase,omitempty"`
	DryRun  bool         `json:"dry_run"`
	OK      bool         `json:"ok"`
	Hosts   []HostResult `json:"hosts"`
}

type plan struct {
	spec  catalog.Spec
	obs   catalog.Observation
	dec   catalog.Decision
	stage catalog.StagePlan
}

// Failed is true when any host could not be contacted or an upgrade command failed.
func (r Report) Failed() bool {
	for _, h := range r.Hosts {
		if h.Error != "" {
			return true
		}
		for _, row := range h.Rows {
			if row.Failed {
				return true
			}
		}
	}
	return false
}

// Summary is the one-line footer.
func (r Report) Summary() string {
	hosts := len(r.Hosts)
	hostWord := "hosts"
	if hosts == 1 {
		hostWord = "host"
	}
	if r.DryRun && r.Command == "status" {
		return fmt.Sprintf("dry-run · %d %s · no connections opened", hosts, hostWord)
	}
	var installed, missing, upgraded, current, skipped, would, failed int
	var staged, nostage, applied int
	seenFail := map[string]bool{}
	for _, h := range r.Hosts {
		hostFailed := h.Error != ""
		for _, row := range h.Rows {
			switch row.Status {
			case StInstalled:
				installed++
			case StMissing:
				missing++
			case StUpgraded:
				upgraded++
			case StApplied:
				applied++
			case StCurrent:
				current++
			case StSkipped:
				skipped++
			case StDryRun:
				would++
			case StStaged:
				staged++
			case StNoStage:
				nostage++
			}
			if row.Failed || row.Status == StUnreachable || row.Status == StFailed {
				hostFailed = true
			}
		}
		if hostFailed && !seenFail[h.Name] {
			seenFail[h.Name] = true
			failed++
		}
	}
	if r.Phase == PhaseFetch && r.DryRun {
		return fmt.Sprintf("dry-run · %d %s · %d would stage · %d no-stage · %d skipped · %d failed",
			hosts, hostWord, would, nostage, skipped+missing, failed)
	}
	if r.Phase == PhaseFetch {
		return fmt.Sprintf("%d %s · %d staged · %d no-stage · %d skipped · %d failed",
			hosts, hostWord, staged, nostage, skipped+missing, failed)
	}
	if r.Phase == PhaseApply && r.DryRun {
		return fmt.Sprintf("dry-run · %d %s · %d would apply · %d no-stage · %d skipped · %d failed",
			hosts, hostWord, would, nostage, skipped+missing, failed)
	}
	if r.Phase == PhaseApply {
		return fmt.Sprintf("%d %s · %d applied · %d up to date · %d no-stage · %d skipped · %d failed",
			hosts, hostWord, applied, current, nostage, skipped+missing, failed)
	}
	if r.Command == "upgrade" && r.DryRun {
		return fmt.Sprintf("dry-run · %d %s · %d would upgrade · %d skipped · %d failed",
			hosts, hostWord, would, skipped+missing, failed)
	}
	if r.Command == "upgrade" {
		return fmt.Sprintf("%d %s · %d upgraded · %d up to date · %d skipped · %d failed",
			hosts, hostWord, upgraded, current, skipped+missing, failed)
	}
	return fmt.Sprintf("%d %s · %d installed · %d not installed · %d failed",
		hosts, hostWord, installed, missing, failed)
}

// JSON renders the report for --json.
func (r Report) JSON() ([]byte, error) {
	r.OK = !r.Failed()
	return json.MarshalIndent(r, "", "  ")
}

// Status probes every selected host, or prints the plan when DryRun is set.
func Status(ctx context.Context, cfg config.Loaded, opt Options, run runner.Runner) (Report, error) {
	return runFleet(ctx, cfg, opt, run, false)
}

// Upgrade probes, then upgrades only the tools that are already installed.
func Upgrade(ctx context.Context, cfg config.Loaded, opt Options, run runner.Runner) (Report, error) {
	return runFleet(ctx, cfg, opt, run, true)
}

func runFleet(ctx context.Context, cfg config.Loaded, opt Options, run runner.Runner, upgrade bool) (Report, error) {
	hosts, err := config.FilterHosts(cfg.Hosts, opt.Hosts)
	if err != nil {
		return Report{}, err
	}
	parallel := cfg.Concurrency
	if opt.Parallel > 0 {
		parallel = opt.Parallel
	}
	if parallel < 1 {
		parallel = 1
	}
	if parallel > config.MaxConcurrency {
		parallel = config.MaxConcurrency
	}
	timeout := cfg.Timeout
	if opt.Timeout > 0 {
		timeout = opt.Timeout
	} else if !upgrade && !cfg.TimeoutExplicit && timeout > StatusBudget {
		timeout = StatusBudget
	}

	command := "status"
	if upgrade {
		command = "upgrade"
	}
	report := Report{Command: command, DryRun: opt.DryRun, Hosts: make([]HostResult, len(hosts))}
	if upgrade && opt.phase() != PhaseUpgrade {
		report.Phase = opt.phase()
	}
	overlays := overlaysFrom(cfg)

	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i := range hosts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				report.Hosts[i] = hostError(hosts[i], ctx.Err().Error())
				return
			}
			report.Hosts[i] = oneHost(ctx, cfg, hosts[i], opt, timeout, overlays, run, upgrade)
		}(i)
	}
	wg.Wait()
	report.OK = !report.Failed()
	return report, nil
}

func overlaysFrom(cfg config.Loaded) []catalog.Overlay {
	out := make([]catalog.Overlay, len(cfg.Recipes))
	for i, r := range cfg.Recipes {
		out[i] = catalog.Overlay{
			Name: r.Name, Binary: r.Binary, Native: append([]string(nil), r.Native...),
			Npm: r.Npm, Brew: r.Brew, BrewKind: r.BrewKind,
		}
	}
	return out
}

func oneHost(ctx context.Context, cfg config.Loaded, host config.Host, opt Options, timeout time.Duration, overlays []catalog.Overlay, run runner.Runner, upgrade bool) HostResult {
	dest, port, err := config.Destination(host)
	if err != nil {
		return hostError(host, err.Error())
	}
	result := HostResult{Name: host.Name, Destination: dest, Local: host.Local}
	names := catalog.Candidates(cfg.Tools, catalog.HostPolicy{Tools: host.Tools, Skip: host.Skip}, opt.Only)
	if len(names) == 0 {
		return result
	}
	if !upgrade && opt.DryRun {
		for _, name := range names {
			result.Rows = append(result.Rows, Row{Tool: name, Status: StWouldProbe})
		}
		return result
	}

	tools := make([]probe.Tool, len(names))
	specs := make([]catalog.Spec, len(names))
	for i, name := range names {
		specs[i] = catalog.Resolve(name, overlays)
		tools[i] = probe.Tool{Name: specs[i].Name, Binary: specs[i].Binary}
	}

	if timeout > 0 {
		progress(opt, "→ %s (%s)", host.Name, timeout.Round(time.Second))
	} else {
		progress(opt, "→ %s", host.Name)
	}
	script, err := probe.ProbeScript(tools, host.PathExtra)
	if err != nil {
		progress(opt, "✗ %s  %s", host.Name, err.Error())
		return hostError(host, err.Error())
	}
	target := runner.Target{
		Name: host.Name, Local: host.Local, Destination: dest,
		Port: port, Identity: host.Identity, Timeout: timeout,
	}
	stdout, stderr, err := run.Run(ctx, target, script)
	if err != nil {
		progress(opt, "✗ %s  %s", host.Name, err.Error())
		return unreachable(host, names, err.Error())
	}
	obs, err := probe.ParseProbe(stdout)
	if err != nil {
		msg := err.Error()
		if line := firstLine(stderr); line != "" {
			msg += ": " + line
		}
		progress(opt, "✗ %s  %s", host.Name, msg)
		return unreachable(host, names, msg)
	}

	phase := PhaseUpgrade
	if upgrade {
		phase = opt.phase()
	}
	plans := make([]plan, len(names))
	var steps []probe.Step
	var staged []probe.StageStep
	for i, name := range names {
		o, ok := obs[name]
		if !ok {
			plans[i] = plan{spec: specs[i], dec: catalog.Decision{Fail: "probe did not return " + name}}
			continue
		}
		override := host.Methods[name]
		dec := catalog.Decide(specs[i], o, override)
		p := plan{spec: specs[i], obs: o, dec: dec}
		if phase != PhaseUpgrade {
			p.stage = catalog.PlanStage(specs[i], o, override)
			if p.stage.Action == catalog.ActionStage {
				staged = append(staged, probe.StageStep{
					Tool: name, Kind: p.stage.Kind, Package: p.stage.Package,
					BrewKind: p.stage.BrewKind, BrewToken: p.stage.BrewToken,
				})
			}
		}
		plans[i] = p
		// Only an installed tool with a concrete command is eligible.
		// Decide returns a nil command when the binary is missing.
		if phase == PhaseUpgrade && o.Present && dec.Command != nil && dec.Fail == "" {
			steps = append(steps, probe.Step{Tool: name, Cmd: *dec.Command})
		}
	}
	if phase == PhaseFetch || phase == PhaseApply {
		return finishStage(ctx, result, target, run, opt, phase, names, plans, staged, tools, host.PathExtra)
	}

	var exits map[string]int
	var after map[string]catalog.Observation
	if upgrade && !opt.DryRun && len(steps) > 0 {
		upScript, err := probe.UpgradeScriptSteps(tools, host.PathExtra, steps)
		if err != nil {
			progress(opt, "✗ %s  %s", host.Name, err.Error())
			return hostError(host, err.Error())
		}
		upOut, upErr, err := run.Run(ctx, target, upScript)
		if err != nil {
			progress(opt, "✗ %s  %s", host.Name, err.Error())
			failed := hostError(host, err.Error())
			for i, name := range names {
				failed.Rows = append(failed.Rows, failedRow(name, plans[i], err.Error()))
			}
			return failed
		}
		results, probed, perr := probe.ParseUpgrade(upOut)
		if perr != nil {
			progress(opt, "✗ %s  %s", host.Name, perr.Error())
			failed := hostError(host, perr.Error())
			for _, name := range names {
				failed.Rows = append(failed.Rows, Row{Tool: name, Status: StFailed, Detail: perr.Error(), Failed: true})
			}
			return failed
		}
		exits = map[string]int{}
		for _, res := range results {
			exits[res.Tool] = res.Exit
		}
		after = probed
		if opt.Verbose {
			result.Log = strings.TrimSpace(upErr)
		}
	}

	for i, name := range names {
		result.Rows = append(result.Rows, renderRow(name, plans[i], upgrade, opt.DryRun, exits, after))
	}
	if hostBad(result) {
		progress(opt, "✗ %s", host.Name)
	} else {
		progress(opt, "✓ %s", host.Name)
	}
	return result
}

func renderRow(name string, p plan, upgrade, dry bool, exits map[string]int, after map[string]catalog.Observation) Row {
	if strings.HasPrefix(p.dec.Fail, "probe did not") {
		return Row{Tool: name, Status: StFailed, Detail: p.dec.Fail, Failed: true}
	}
	if !p.obs.Present {
		// Hard rule: a missing binary produces no command and no install.
		return Row{Tool: name, Status: StMissing, Installed: boolPtr(false)}
	}
	if p.dec.Fail != "" && p.dec.Command == nil {
		return presentRow(name, p, StFailed, p.dec.Fail, true)
	}
	if p.dec.Command == nil {
		reason := p.dec.Skip
		if reason == "" || reason == "not installed" {
			reason = "no upgrade recipe"
		}
		if !upgrade {
			// status still reports the install even when we cannot upgrade it
			row := presentRow(name, p, StInstalled, "", false)
			if p.dec.Method != "" {
				row.Method = p.dec.Method
			}
			return row
		}
		return presentRow(name, p, StSkipped, reason, false)
	}

	row := presentRow(name, p, StInstalled, "", false)
	row.Command = p.dec.Command.Display
	row.Method = p.dec.Method
	if !upgrade {
		return row
	}
	if dry {
		row.Status = StDryRun
		row.Detail = p.dec.Command.Display
		return row
	}
	code, ok := exits[name]
	if !ok {
		row.Status = StFailed
		row.Detail = "upgrade did not report a result"
		row.Failed = true
		return row
	}
	if code != 0 {
		row.Status = StFailed
		row.Detail = fmt.Sprintf("exit %d", code)
		row.Failed = true
		return row
	}
	before := row.Version
	if next, ok := after[name]; ok {
		return noteVersion(row, before, next, StUpgraded)
	}
	row.Status = StUpgraded
	return row
}

func noteVersion(row Row, before string, next catalog.Observation, changed string) Row {
	if !next.Present {
		row.Status = changed
		return row
	}
	afterV := probe.ShortVersion(next.Version)
	if afterV == "" {
		afterV = "unknown"
	}
	row.VersionRaw = next.Version
	if next.Path != "" {
		row.Path = next.Path
	}
	if afterV == before {
		row.Status = StCurrent
		return row
	}
	row.Status = changed
	row.Version = before + " → " + afterV
	return row
}

func presentRow(name string, p plan, status, detail string, failed bool) Row {
	ver := probe.ShortVersion(p.obs.Version)
	if ver == "" {
		ver = "unknown"
	}
	method := p.dec.Method
	if method == "" {
		method = catalog.Classify(p.obs)
	}
	return Row{
		Tool: name, Version: ver, VersionRaw: p.obs.Version, Method: method,
		Status: status, Detail: detail, Path: p.obs.Path, Failed: failed,
		Installed: boolPtr(true),
	}
}

func failedRow(name string, p plan, detail string) Row {
	if !p.obs.Present {
		return Row{Tool: name, Status: StMissing, Installed: boolPtr(false)}
	}
	row := presentRow(name, p, StFailed, detail, true)
	return row
}

func unreachable(h config.Host, names []string, msg string) HostResult {
	out := hostError(h, msg)
	short := firstLine(msg)
	if len(short) > 100 {
		short = short[:100] + "…"
	}
	for _, name := range names {
		out.Rows = append(out.Rows, Row{Tool: name, Status: StUnreachable, Detail: short, Failed: true})
	}
	return out
}

func hostBad(h HostResult) bool {
	if h.Error != "" {
		return true
	}
	for _, row := range h.Rows {
		if row.Failed {
			return true
		}
	}
	return false
}

func hostError(h config.Host, msg string) HostResult {
	dest, _, _ := config.Destination(h)
	return HostResult{Name: h.Name, Destination: dest, Local: h.Local, Error: msg}
}

func progress(opt Options, format string, args ...any) {
	if opt.OnProgress == nil {
		return
	}
	opt.OnProgress(fmt.Sprintf(format, args...))
}

func boolPtr(v bool) *bool { return &v }

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// Lines renders plain table rows in host order.
func (r Report) Lines() [][]string {
	var out [][]string
	for _, h := range r.Hosts {
		if len(h.Rows) == 0 && h.Error != "" {
			out = append(out, []string{h.Name, "—", "—", "—", h.Error})
			continue
		}
		for _, row := range h.Rows {
			version := row.Version
			method := row.Method
			if version == "" {
				version = "—"
			}
			if method == "" {
				method = "—"
			}
			status := row.Status
			switch row.Status {
			case StDryRun:
				if row.Detail != "" {
					status = "dry-run: " + row.Detail
				}
			case StSkipped, StFailed, StNoStage, StStaged, StApplied, StUnreachable:
				if row.Detail != "" {
					status = row.Status + " (" + row.Detail + ")"
				}
			}
			out = append(out, []string{h.Name, row.Tool, version, method, status})
		}
	}
	return out
}

// Errors lists host-level failures in fleet order.
func (r Report) Errors() []string {
	var out []string
	seen := map[string]struct{}{}
	for _, h := range r.Hosts {
		if h.Error == "" {
			continue
		}
		line := h.Name + ": " + firstLine(h.Error)
		if _, ok := seen[line]; ok {
			continue
		}
		seen[line] = struct{}{}
		out = append(out, line)
	}
	return out
}
