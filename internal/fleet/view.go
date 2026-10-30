package fleet

import (
	"fmt"
	"strings"

	"github.com/indiekitai/clifleet/internal/semver"
)

// Matrix is the status view: one row per tool, one column per host.
type Matrix struct {
	Header []string
	Rows   [][]string
	// NotInstalled lists tools no selected host has.
	NotInstalled []string
	Legend       string
}

const (
	cellCurrent = "✓"
	cellNone    = "·"
	cellUnknown = "?"
	cellDown    = "✗"
)

// Matrix builds the tool × host view of a status report. ok is false when
// the report has no probe results to show (a dry run, or nothing to check).
// color paints "update available" cells yellow.
func (r Report) Matrix(color bool) (Matrix, bool) {
	if r.Command != "status" || r.DryRun || len(r.Hosts) == 0 {
		return Matrix{}, false
	}
	var tools []string
	seen := map[string]bool{}
	byHost := make([]map[string]Row, len(r.Hosts))
	for i, h := range r.Hosts {
		byHost[i] = map[string]Row{}
		for _, row := range h.Rows {
			if row.Status == StWouldProbe {
				return Matrix{}, false
			}
			byHost[i][row.Tool] = row
			if !seen[row.Tool] {
				seen[row.Tool] = true
				tools = append(tools, row.Tool)
			}
		}
	}
	if len(tools) == 0 {
		return Matrix{}, false
	}
	m := Matrix{Header: []string{"TOOL", "LATEST"}}
	for _, h := range r.Hosts {
		m.Header = append(m.Header, h.Name)
	}
	used := map[string]bool{}
	for _, tool := range tools {
		newest := ""
		line := []string{tool, ""}
		present := false
		var kinds []string
		for i, h := range r.Hosts {
			row, has := byHost[i][tool]
			if has && row.Latest != "" {
				newest = higher(newest, row.Latest)
			}
			text, kind := matrixCell(row, has, h.Error != "")
			kinds = append(kinds, kind)
			if kind != "none" {
				present = true
			}
			if kind == "behind" && color {
				text = "\033[33m" + text + "\033[0m"
			}
			line = append(line, text)
		}
		if !present {
			m.NotInstalled = append(m.NotInstalled, tool)
			continue
		}
		for _, k := range kinds {
			used[k] = true
		}
		if newest == "" {
			newest = "—"
		}
		line[1] = newest
		m.Rows = append(m.Rows, line)
	}
	used["none"] = true
	var legend []string
	if used["current"] {
		legend = append(legend, cellCurrent+" up to date")
	}
	if used["behind"] || used["nostage"] {
		legend = append(legend, "↑ <version> behind LATEST")
	}
	if used["version"] {
		legend = append(legend, "<version> installed, newest unknown")
	}
	if used["unknown"] {
		legend = append(legend, cellUnknown+" version unreadable")
	}
	legend = append(legend, cellNone+" not installed")
	if used["down"] {
		legend = append(legend, cellDown+" unreachable or failed")
	}
	m.Legend = strings.Join(legend, " · ")
	if used["nostage"] {
		m.Legend += " · * only upgrade --one-shot can update it"
	}
	return m, true
}

// matrixCell returns the cell text and its kind. A host with an error and no
// row for the tool shows as down, not as "not installed".
func matrixCell(row Row, has, hostDown bool) (string, string) {
	if !has {
		if hostDown {
			return cellDown, "down"
		}
		return cellNone, "none"
	}
	switch row.Status {
	case StMissing:
		return cellNone, "none"
	case StCurrent:
		return cellCurrent, "current"
	case StBehind:
		if row.NoStage {
			return "↑ " + row.Version + "*", "nostage"
		}
		return "↑ " + row.Version, "behind"
	case StFailed, StUnreachable:
		return cellDown, "down"
	case StInstalled:
		if row.Version == "" || row.Version == "unknown" || unreadableVersion(row.VersionRaw) {
			return cellUnknown, "unknown"
		}
		return row.Version, "version"
	}
	return cellUnknown, "unknown"
}

// higher returns the newer of two versions, keeping a when they do not order.
func higher(a, b string) string {
	if a == "" {
		return b
	}
	if c, ok := semver.Compare(a, b); ok && c < 0 {
		return b
	}
	return a
}

// NoStageBehind lists tools that are behind on some host and can only be
// moved with --one-shot, as "tool (host, host)" in tool order.
func (r Report) NoStageBehind() []string {
	var order []string
	hosts := map[string][]string{}
	for _, h := range r.Hosts {
		for _, row := range h.Rows {
			if row.Status == StBehind && row.NoStage {
				if _, ok := hosts[row.Tool]; !ok {
					order = append(order, row.Tool)
				}
				hosts[row.Tool] = append(hosts[row.Tool], h.Name)
			}
		}
	}
	out := make([]string, len(order))
	for i, t := range order {
		out[i] = fmt.Sprintf("%s (%s)", t, strings.Join(hosts[t], ", "))
	}
	return out
}

// Count returns how many rows have the given status.
func (r Report) Count(status string) int {
	n := 0
	for _, h := range r.Hosts {
		for _, row := range h.Rows {
			if row.Status == status {
				n++
			}
		}
	}
	return n
}

// Hidden counts the rows Compact left out.
type Hidden struct {
	Total int
	Parts []string
}

// Line is the one-line fold of the hidden rows, or "" when none.
func (h Hidden) Line() string {
	if h.Total == 0 {
		return ""
	}
	return fmt.Sprintf("Not shown (%d rows, --long lists them): %s", h.Total, strings.Join(h.Parts, " · "))
}

// Compact keeps only the rows where something happened or needs attention:
// applied, upgraded, staged, failed, blocked, unreachable, and dry-run rows
// that would act. A host that could not be reached keeps its error row.
func (r Report) Compact() (Report, Hidden) {
	out := r
	out.Hosts = nil
	counts := map[string]int{}
	var h Hidden
	for _, host := range r.Hosts {
		kept := host
		kept.Rows = nil
		for _, row := range host.Rows {
			switch row.Status {
			case StApplied, StUpgraded, StStaged, StFailed, StBlocked, StUnreachable, StDryRun:
				kept.Rows = append(kept.Rows, row)
			default:
				counts[row.Status]++
				h.Total++
			}
		}
		if len(kept.Rows) > 0 || host.Error != "" {
			out.Hosts = append(out.Hosts, kept)
		}
	}
	for _, p := range []struct{ status, label string }{
		{StMissing, "not installed"}, {StCurrent, "up to date"}, {StNoStage, "no-stage"}, {StSkipped, "skipped"},
	} {
		if n := counts[p.status]; n > 0 {
			h.Parts = append(h.Parts, fmt.Sprintf("%d %s", n, p.label))
			delete(counts, p.status)
		}
	}
	other := 0
	for _, n := range counts {
		other += n
	}
	if other > 0 {
		h.Parts = append(h.Parts, fmt.Sprintf("%d other", other))
	}
	return out, h
}

// wouldSay is the dry-run cell in words. The command stays in Row.Command.
func (r Report) wouldSay(row Row) string {
	var verb string
	switch {
	case r.Phase == PhaseApply:
		return "would apply"
	case r.Phase == PhaseFetch:
		verb = "would fetch"
	case r.OneShot:
		verb = "would run updater"
	default:
		verb = "would upgrade"
	}
	if row.Latest != "" {
		verb += " → " + row.Latest
	}
	return verb
}

// FailedTargets lists the hosts and tools behind the failures of a run, in
// fleet order, for a retry command.
func (r Report) FailedTargets() (hosts, tools []string) {
	seenH, seenT := map[string]bool{}, map[string]bool{}
	for _, h := range r.Hosts {
		bad := h.Error != ""
		for _, row := range h.Rows {
			if row.Failed || row.Status == StFailed || row.Status == StUnreachable {
				bad = true
				if row.Status != StUnreachable && !seenT[row.Tool] {
					seenT[row.Tool] = true
					tools = append(tools, row.Tool)
				}
			}
		}
		if bad && !seenH[h.Name] {
			seenH[h.Name] = true
			hosts = append(hosts, h.Name)
		}
	}
	return hosts, tools
}
