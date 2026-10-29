package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/indiekitai/clifleet/internal/fleet"
	"github.com/indiekitai/clifleet/internal/table"
)

const historyHelp = `Usage:
  clifleet history [flags]

Show past upgrade, fetch, and apply runs, one row per host and tool,
oldest first. Every run that is not --dry-run appends to
$XDG_CACHE_HOME/clifleet/history.jsonl (or ~/.cache/clifleet/history.jsonl).
status and --dry-run are not recorded, and neither are tools that were not
installed. A host that failed as a whole has one row with tool —.

Flags:
      --hosts LIST   Only these hosts (comma-separated)
      --only LIST    Only these tools (comma-separated)
      --limit N      Last N rows after filtering (default 50, 0 for all)
      --json         Print the rows as a JSON array
  -h, --help

Examples:
  clifleet history
  clifleet history --hosts us-server --only codex
  clifleet history --limit 0 --json
`

// historyEntry is one line of history.jsonl: one host × tool of one run.
type historyEntry struct {
	Time    string `json:"time"`
	Command string `json:"command"`
	Phase   string `json:"phase,omitempty"`
	Host    string `json:"host"`
	Tool    string `json:"tool,omitempty"`
	Version string `json:"version,omitempty"`
	Method  string `json:"method,omitempty"`
	Status  string `json:"status"`
	Detail  string `json:"detail,omitempty"`
	Failed  bool   `json:"failed,omitempty"`
}

// hostErrorStatus marks a history row for a host-level failure.
const hostErrorStatus = "error"

func historyEntries(report fleet.Report, at time.Time) []historyEntry {
	ts := at.UTC().Format(time.RFC3339)
	var out []historyEntry
	for _, h := range report.Hosts {
		base := historyEntry{Time: ts, Command: report.Command, Phase: report.Phase, Host: h.Name}
		if h.Error != "" {
			e := base
			e.Status = hostErrorStatus
			e.Detail = h.Error
			e.Failed = true
			out = append(out, e)
		}
		for _, row := range h.Rows {
			if row.Status == fleet.StMissing {
				continue
			}
			e := base
			e.Tool = row.Tool
			e.Version = row.Version
			e.Method = row.Method
			e.Status = row.Status
			e.Detail = row.Detail
			e.Failed = row.Failed || row.Status == fleet.StFailed || row.Status == fleet.StUnreachable
			out = append(out, e)
		}
	}
	return out
}

// appendHistory adds one run to history.jsonl. The whole run goes out in a
// single O_APPEND write, so two runs finishing together do not interleave
// lines. Dry runs and status are not history.
func appendHistory(report fleet.Report, at time.Time) error {
	if report.DryRun || report.Command != "upgrade" {
		return nil
	}
	entries := historyEntries(report, at)
	if len(entries) == 0 {
		return nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	for _, e := range entries {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	path, err := cachePath("history.jsonl")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(buf.Bytes())
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// readHistory returns every entry it can parse. A line cut short by a crash
// or edited by hand is skipped, not fatal.
func readHistory(path string) ([]historyEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []historyEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var e historyEntry
		if json.Unmarshal(line, &e) != nil || e.Host == "" || e.Status == "" {
			continue
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

func cmdHistory(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("clifleet", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, historyHelp) }
	var hosts, only stringList
	var limit int
	var jsonOut bool
	fs.Var(&hosts, "hosts", "comma-separated hosts")
	fs.Var(&only, "only", "comma-separated tools")
	fs.IntVar(&limit, "limit", 50, "last N rows")
	fs.BoolVar(&jsonOut, "json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "clifleet history: unexpected %s\n", strings.Join(fs.Args(), " "))
		return 1
	}
	if limit < 0 {
		fmt.Fprintln(stderr, "clifleet history: limit must be 0 or more")
		return 1
	}
	path, err := cachePath("history.jsonl")
	if err != nil {
		fmt.Fprintf(stderr, "clifleet: %s\n", err)
		return 1
	}
	all, err := readHistory(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stderr, "clifleet: %s\n", err)
		return 1
	}
	var rows []historyEntry
	for _, e := range all {
		if len(hosts) > 0 && !contains(hosts, e.Host) {
			continue
		}
		if len(only) > 0 && !contains(only, e.Tool) {
			continue
		}
		rows = append(rows, e)
	}
	if limit > 0 && len(rows) > limit {
		rows = rows[len(rows)-limit:]
	}
	if jsonOut {
		if rows == nil {
			rows = []historyEntry{}
		}
		raw, err := json.MarshalIndent(rows, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "clifleet: %s\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "%s\n", raw)
		return 0
	}
	if len(rows) == 0 {
		fmt.Fprintf(stdout, "no history yet (%s)\n", path)
		return 0
	}
	lines := make([][]string, len(rows))
	for i, e := range rows {
		lines[i] = historyLine(e)
	}
	fmt.Fprintln(stdout, table.Render([]string{"TIME", "HOST", "TOOL", "VERSION", "METHOD", "PHASE", "STATUS"}, lines, colorEnabled(stdout)))
	return 0
}

func historyLine(e historyEntry) []string {
	when := e.Time
	if t, err := time.Parse(time.RFC3339, e.Time); err == nil {
		when = t.Local().Format("2006-01-02 15:04")
	}
	status := e.Status
	if e.Detail != "" {
		status += " (" + e.Detail + ")"
	}
	cells := []string{when, e.Host, e.Tool, e.Version, e.Method, e.Phase, status}
	for i, c := range cells {
		if c == "" {
			c = "—"
		}
		cells[i] = printable(c)
	}
	return cells
}

// printable keeps a row on one line: remote detail text can carry
// newlines, tabs, or escape sequences.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return ' '
		}
		return r
	}, s)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
