package cli

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/indiekitai/clifleet/internal/fleet"
	"github.com/indiekitai/clifleet/internal/table"
)

// termWidth is the width a table may use: $COLUMNS when set, 120 on a
// terminal, and 0 (no limit) when stdout is not a terminal.
func termWidth(w io.Writer) int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("COLUMNS"))); err == nil && v > 0 {
		return v
	}
	if isTerm(w) {
		return 120
	}
	return 0
}

func widest(s string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		if w := utf8.RuneCountInString(line); w > n {
			n = w
		}
	}
	return n
}

// printStatus writes the status output: the tool × host matrix, or the long
// per-row table when --long is set, the matrix is wider than width, or there
// is no probe result to pivot. width 0 means no limit.
func printStatus(w io.Writer, report fleet.Report, long bool, width int, color bool) {
	if !long {
		if m, ok := report.Matrix(false); ok {
			rendered := table.Render(m.Header, m.Rows, false)
			if len(m.Rows) == 0 || width == 0 || widest(rendered) <= width {
				if len(m.Rows) > 0 {
					if color {
						m, _ = report.Matrix(true)
					}
					fmt.Fprintln(w, table.Render(m.Header, m.Rows, false))
					fmt.Fprintln(w, m.Legend)
				}
				if len(m.NotInstalled) > 0 {
					fmt.Fprintf(w, "not installed anywhere: %s\n", strings.Join(m.NotInstalled, ", "))
				}
				fmt.Fprintln(w, report.Summary())
				return
			}
		}
	}
	printLong(w, report, color, false)
}

func printLong(w io.Writer, report fleet.Report, color, words bool) {
	lines := report.Lines()
	if words {
		lines = report.CompactLines()
	}
	if len(lines) == 0 {
		fmt.Fprintln(w, "no tools to check on the selected hosts")
	} else {
		fmt.Fprintln(w, table.Render(report.Header(), lines, color))
	}
	fmt.Fprintln(w, report.Summary())
}

// printUpgrade writes the upgrade output. Unless long is set, rows where
// nothing happened fold into one line and dry-run cells say what would
// happen. -v keeps the commands in the table.
func printUpgrade(w io.Writer, report fleet.Report, long, verbose, color bool) {
	if long {
		printLong(w, report, color, false)
		return
	}
	shown, hidden := report.Compact()
	lines := shown.Lines()
	if !verbose {
		lines = shown.CompactLines()
	}
	switch {
	case len(report.Lines()) == 0:
		fmt.Fprintln(w, "no tools to check on the selected hosts")
	case len(lines) == 0:
		fmt.Fprintln(w, "Nothing to act on or report.")
	default:
		fmt.Fprintln(w, table.Render(shown.Header(), lines, color))
	}
	if line := hidden.Line(); line != "" {
		fmt.Fprintln(w, line)
	}
	fmt.Fprintln(w, report.Summary())
}

// nextHints are the "what now" lines after a run. The caller prints them
// only on a terminal and never with --json.
func nextHints(report fleet.Report, phase string, oneShot bool) []string {
	var out []string
	if report.Command == "status" {
		if report.DryRun {
			return nil
		}
		if report.Count(fleet.StBehind) > 0 {
			out = append(out, "Next: clifleet upgrade --fetch (download only), then clifleet upgrade --apply.")
			if only := report.NoStageBehind(); len(only) > 0 {
				out = append(out, "Only --one-shot can update: "+strings.Join(only, ", "))
			}
		}
		return out
	}
	if phase == fleet.PhaseFetch && !report.DryRun && report.Count(fleet.StStaged) > 0 {
		out = append(out, "Next: clifleet upgrade --apply")
	}
	if report.Failed() {
		mode := ""
		switch {
		case phase == fleet.PhaseFetch:
			mode = " --fetch"
		case phase == fleet.PhaseApply:
			mode = " --apply"
		case oneShot:
			mode = " --one-shot"
		}
		hosts, tools := report.FailedTargets()
		retry := "clifleet upgrade" + mode
		if len(hosts) > 0 {
			retry += " --hosts " + strings.Join(hosts, ",")
		}
		if len(tools) > 0 {
			retry += " --only " + strings.Join(tools, ",")
		}
		out = append(out, "Details: clifleet history --limit 20. Retry: "+retry)
	}
	return out
}
