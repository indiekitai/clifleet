// Package fleet fans a status or upgrade out across hosts.
package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/indiekitai/clifleet/internal/catalog"
	"github.com/indiekitai/clifleet/internal/config"
	"github.com/indiekitai/clifleet/internal/latest"
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
	StBlocked     = "blocked"
	StBehind      = "update available"
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
	OneShot    bool
	OnProgress func(string)
	// Latest returns the newest version on a channel, or "" when unknown.
	// nil skips the lookup: no LATEST column, and fetch downloads every
	// stageable tool.
	Latest func(context.Context, latest.Query) string
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
	Latest     string `json:"latest,omitempty"`
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
	// latest is the newest version the control machine found. current means
	// the installed version already is that one on the fetch channel, so the
	// tool is not fetched.
	latest  string
	current bool
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
	var installed, missing, upgraded, current, skipped, would, failedTools, failedHosts int
	var staged, nostage, applied, blocked, behind int
	seenFail := map[string]bool{}
	for _, h := range r.Hosts {
		hostFailed := h.Error != ""
		toolFails := 0
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
			case StBehind:
				behind++
			case StSkipped:
				skipped++
			case StDryRun:
				would++
			case StStaged:
				staged++
			case StNoStage:
				nostage++
			case StBlocked:
				blocked++
			}
			if row.Failed || row.Status == StUnreachable || row.Status == StFailed {
				toolFails++
				hostFailed = true
			}
		}
		failedTools += toolFails
		if hostFailed && !seenFail[h.Name] {
			seenFail[h.Name] = true
			failedHosts++
			if toolFails == 0 {
				failedTools++
			}
		}
	}
	failed := failedPhrase(failedTools, failedHosts)
	if r.Phase == PhaseFetch && r.DryRun {
		return fmt.Sprintf("dry-run · %d %s · %d would stage · %d blocked · %d no-stage · %d skipped · %s",
			hosts, hostWord, would, blocked, nostage, skipped+missing, failed)
	}
	if r.Phase == PhaseFetch {
		return fmt.Sprintf("%d %s · %d staged · %d blocked · %d no-stage · %d skipped · %s",
			hosts, hostWord, staged, blocked, nostage, skipped+missing, failed)
	}
	if r.Phase == PhaseApply && r.DryRun {
		return fmt.Sprintf("dry-run · %d %s · %d would apply · %d blocked · %d no-stage · %d skipped · %s",
			hosts, hostWord, would, blocked, nostage, skipped+missing, failed)
	}
	if r.Phase == PhaseApply {
		return fmt.Sprintf("%d %s · %d applied · %d up to date · %d blocked · %d no-stage · %d skipped · %s",
			hosts, hostWord, applied, current, blocked, nostage, skipped+missing, failed)
	}
	if r.Command == "upgrade" && r.DryRun {
		return fmt.Sprintf("dry-run · %d %s · %d would upgrade · %d no-stage · %d blocked · %d skipped · %s",
			hosts, hostWord, would, nostage, blocked, skipped+missing, failed)
	}
	if r.Command == "upgrade" {
		return fmt.Sprintf("%d %s · %d upgraded · %d up to date · %d no-stage · %d blocked · %d skipped · %s",
			hosts, hostWord, upgraded+applied, current, nostage, blocked, skipped+missing, failed)
	}
	if current+behind > 0 {
		return fmt.Sprintf("%d %s · %d installed · %d update available · %d not installed · %s",
			hosts, hostWord, installed+current+behind, behind, missing, failed)
	}
	return fmt.Sprintf("%d %s · %d installed · %d not installed · %s",
		hosts, hostWord, installed, missing, failed)
}

// failedPhrase counts tool rows. When that number and the number of hosts
// with a failure differ, the host count is printed too so "1 failed" is not
// read as one host that had several tool failures.
func failedPhrase(tools, hosts int) string {
	if hosts > 0 && tools != hosts {
		word := "hosts"
		if hosts == 1 {
			word = "host"
		}
		return fmt.Sprintf("%d failed (%d %s)", tools, hosts, word)
	}
	return fmt.Sprintf("%d failed", tools)
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
				report.Hosts[i] = hostError(hosts[i], "interrupted before this host started")
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
		if errors.Is(err, runner.ErrInterrupted) {
			// The host answered or never got asked; it is not unreachable.
			return downRows(host, names, err.Error(), StFailed)
		}
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
	// The default upgrade downloads, then swaps. --one-shot is the in-place path.
	safe := phase == PhaseUpgrade && upgrade && !opt.OneShot
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
		if phase != PhaseUpgrade || safe {
			p.stage = catalog.PlanStage(specs[i], o, override)
		}
		plans[i] = p
		// Only an installed tool with a concrete command is eligible.
		// Decide returns a nil command when the binary is missing.
		if phase == PhaseUpgrade && !safe && o.Present && dec.Command != nil && dec.Fail == "" {
			steps = append(steps, probe.Step{Tool: name, Cmd: *dec.Command})
		}
	}
	// Apply installs what fetch staged and checks the version itself; only a
	// download is skipped for a tool that is already current.
	markLatest(ctx, opt, plans, host.Methods, phase == PhaseFetch || safe)
	for i, name := range names {
		p := plans[i]
		if p.stage.Action == catalog.ActionStage && !p.current {
			staged = append(staged, probe.StageStep{
				Tool: name, Kind: p.stage.Kind, Package: p.stage.Package,
				BrewKind: p.stage.BrewKind, BrewToken: p.stage.BrewToken,
				Release: p.stage.Release,
			})
		}
	}
	if phase == PhaseFetch || phase == PhaseApply {
		return withLatest(finishStage(ctx, result, target, run, opt, phase, names, plans, staged, tools, host.PathExtra), plans, false)
	}
	if safe {
		return withLatest(finishSafe(ctx, result, target, run, opt, names, plans, staged, tools, host.PathExtra), plans, false)
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
			// Keep the rows of tools whose updater finished before the host error;
			// only the ones that never reported carry the error.
			failed := hostError(host, err.Error())
			partial, _, _ := probe.ParseUpgrade(completeLines(upOut))
			done := map[string]int{}
			for _, res := range partial {
				done[res.Tool] = res.Exit
			}
			for i, name := range names {
				if _, ok := done[name]; !ok && plans[i].dec.Command != nil {
					failed.Rows = append(failed.Rows, failedRow(name, plans[i], err.Error()))
					continue
				}
				failed.Rows = append(failed.Rows, renderRow(name, plans[i], upgrade, false, done, nil))
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
	return withLatest(result, plans, !upgrade)
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
	if unreadableVersion(next.Version) {
		row.Status = changed
		if next.Path != "" {
			row.Path = next.Path
		}
		note := strings.TrimSpace(next.Version)
		if row.Detail == "" {
			row.Detail = note
		} else if !strings.Contains(row.Detail, note) {
			row.Detail = row.Detail + "; " + note
		}
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

func unreadableVersion(raw string) bool {
	v := strings.TrimSpace(raw)
	return v == "version timed out" || v == "keychain locked"
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
	return downRows(h, names, msg, StUnreachable)
}

// downRows fails every tool on a host whose probe did not come back.
func downRows(h config.Host, names []string, msg, status string) HostResult {
	out := hostError(h, msg)
	short := firstLine(msg)
	if len(short) > 100 {
		short = short[:100] + "…"
	}
	for _, name := range names {
		out.Rows = append(out.Rows, Row{Tool: name, Status: status, Detail: short, Failed: true})
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
// The LATEST column only shows when some row has a newest version.
func (r Report) Lines() [][]string {
	withLatest := r.hasLatest()
	var out [][]string
	for _, h := range r.Hosts {
		if len(h.Rows) == 0 && h.Error != "" {
			line := []string{h.Name, "—", "—", "—", h.Error}
			if withLatest {
				line = []string{h.Name, "—", "—", "—", "—", h.Error}
			}
			out = append(out, line)
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
			case StSkipped, StFailed, StNoStage, StStaged, StApplied, StUnreachable, StBlocked:
				if row.Detail != "" {
					status = row.Status + " (" + row.Detail + ")"
				}
			}
			if withLatest {
				newest := row.Latest
				if newest == "" {
					newest = "—"
				}
				out = append(out, []string{h.Name, row.Tool, version, newest, method, status})
				continue
			}
			out = append(out, []string{h.Name, row.Tool, version, method, status})
		}
	}
	return out
}

// Header names the columns of Lines.
func (r Report) Header() []string {
	if r.hasLatest() {
		return []string{"HOST", "TOOL", "VERSION", "LATEST", "METHOD", "STATUS"}
	}
	return []string{"HOST", "TOOL", "VERSION", "METHOD", "STATUS"}
}

func (r Report) hasLatest() bool {
	for _, h := range r.Hosts {
		for _, row := range h.Rows {
			if row.Latest != "" {
				return true
			}
		}
	}
	return false
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
