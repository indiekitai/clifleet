// Package cli is the clifleet command line.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/indiekitai/clifleet/internal/config"
	"github.com/indiekitai/clifleet/internal/fleet"
	"github.com/indiekitai/clifleet/internal/latest"
	"github.com/indiekitai/clifleet/internal/runner"
	"github.com/indiekitai/clifleet/internal/table"
)

// Version is stamped by release builds with -ldflags -X. Other builds
// report what version() finds.
var Version = ""

// Execute runs clifleet and returns the process exit code.
func Execute(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, rootHelp)
		return 0
	}
	if args[0] == "help" {
		if len(args) == 1 {
			fmt.Fprint(stdout, rootHelp)
			return 0
		}
		return Execute([]string{args[1], "--help"}, stdout, stderr)
	}
	switch args[0] {
	case "-V", "--version", "version":
		fmt.Fprintf(stdout, "clifleet %s\n", version())
		return 0
	case "init":
		return cmdInit(args[1:], stdout, stderr)
	case "status":
		return cmdFleet(args[1:], stdout, stderr, false)
	case "upgrade":
		return cmdFleet(args[1:], stdout, stderr, true)
	case "fetch":
		return cmdFleet(append([]string{"--fetch"}, args[1:]...), stdout, stderr, true)
	case "apply":
		return cmdFleet(append([]string{"--apply"}, args[1:]...), stdout, stderr, true)
	case "doctor":
		return cmdDoctor(args[1:], stdout, stderr)
	case "history":
		return cmdHistory(args[1:], stdout, stderr)
	case "update", "self-update":
		return cmdSelfUpdate(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "clifleet: unknown command %q\n\n%s", args[0], rootHelp)
		return 1
	}
}

const rootHelp = `clifleet — Upgrade your AI CLIs across every machine

Usage:
  clifleet <command> [flags]

Commands:
  init         Write an example fleet file
  status       Show host × tool × version × install method
  upgrade      Upgrade the CLIs already installed on each host
  fetch        Stage upgrade artifacts (same as upgrade --fetch)
  apply        Install staged artifacts (same as upgrade --apply)
  doctor       Check SSH, the fleet file, and reachability
  history      Show past upgrade, fetch, and apply runs
  update       Update clifleet itself to the latest release (not the hosts)
  version      Print the clifleet version

clifleet looks at each machine separately. A tool that is not installed
there is skipped. Nothing in the candidate list is installed for you,
and a host allowlist or blocklist cannot be widened from the command line.

Examples:
  clifleet init
  clifleet status
  clifleet status --dry-run
  clifleet upgrade --dry-run
  clifleet upgrade --fetch
  clifleet upgrade --apply
  clifleet upgrade --only claude --hosts us-server
  clifleet doctor
  clifleet history --hosts us-server
  clifleet update --check

Run 'clifleet <command> --help' for command flags.
`

const initHelp = `Usage:
  clifleet init [--config PATH] [--force]

Write an example fleet file. The default path is ~/.config/clifleet/hosts.toml
(or $XDG_CONFIG_HOME/clifleet/hosts.toml). Existing files are left alone
unless --force is set.

The example is heterogeneous on purpose: a US host allowlists claude and
codex, another host lists a regional CLI, and a third skips opencode.
Edit it before you run status or upgrade.

Flags:
  -c, --config PATH   Fleet file to write
      --force         Replace an existing file
  -h, --help
`

const statusHelp = `Usage:
  clifleet status [flags]

SSH to each host, detect which candidate CLIs are installed, and print
host × tool × version × install method. Missing tools show as — and
"not installed". They are not a failure.

LATEST is the newest version on the channel each tool was installed from
(npm registry, Homebrew API, or the vendor's release endpoint), asked once
per tool from this machine, not from the hosts. A tool on the newest
version is "up to date", an older one "update available". A lookup that
fails leaves LATEST empty and the row "installed"; it never fails status.
--no-latest skips the lookups.

--dry-run does not open connections. It prints the tools clifleet would
look for after allowlists, blocklists, and --only.

--only narrows the candidate list. It cannot add a tool that a host
allowlist or blocklist already excluded. A missing tool is "not installed",
not a failure, and nothing is installed for you.

Each --version is capped at a few seconds, and nvm is loaded from a
child process so it cannot read this script or hang the host. doctor can
succeed and status can still fail: doctor only runs true. The timeout
error says whether the probe script started.

Flags:
  -c, --config PATH      Fleet file (default ~/.config/clifleet/hosts.toml)
      --hosts LIST       Comma-separated host names or SSH aliases
      --only LIST        Comma-separated tools to look for
      --timeout DURATION Per host. Default 45s when the fleet file omits timeout, otherwise that value
      --parallel N       Concurrent sessions (default 8, max 64)
      --dry-run          Print the probe plan; do not connect
      --no-latest        Do not look up the newest versions
      --json             Machine-readable matrix
  -v, --verbose          Include remote stderr after the table
  -h, --help

Examples:
  clifleet status
  clifleet status --dry-run
  clifleet status --hosts us-server --only claude
  clifleet status --json
`

const upgradeHelp = `Usage:
  clifleet upgrade [flags]
  clifleet fetch [flags]
  clifleet apply [flags]

On each host, upgrade the candidate CLIs that are already installed.
The default downloads first and then swaps, one tool at a time, so a new
terminal is not opened against a binary that is mid-replace:

  npm                  npm pack @latest, install into a temp prefix, swap the package dir
  Homebrew             brew fetch, then brew upgrade (auto-update off)
  native Codex         GitHub release codex-package-<platform>.tar.gz, then swap
  native Claude Code   downloads.claude.ai manifest + sha256, then retarget the version symlink
  native Cursor Agent  downloads.cursor.com lab tarball, then retarget the version symlink

--fetch and --apply run those two phases on their own. clifleet fetch and
clifleet apply are the same steps. --fetch does not replace the live binary.
--fetch and --apply together are an error.

A tool with no staged artifact (most other native updaters, and bun, pnpm,
and yarn globals) is marked no-stage and left alone. clifleet upgrade
--one-shot runs that updater in place (claude update, codex update, and the
rest). That is the path that can leave a binary missing while it downloads.

The built-in catalog is wider than those channels. Each tool uses the
install method on disk. A tool with no recipe for that channel is skipped.
Opt-in names (dsh, grok, kilo, kiro-cli) are not searched unless the fleet
file lists them.

A missing CLI is skipped. clifleet will not brew install, npm install a
tool that is not already on PATH, or curl an installer. Commands run
without a TTY (stdin is /dev/null) and with non-interactive brew and npm
settings. The timeout is per tool for the default upgrade, --fetch, and
--apply, so one slow download cannot fail the other tools on that host.
Release and Homebrew fetches use that budget when it is longer than the
built-in cap. The exit code is non-zero if any host cannot be reached or
any upgrade command fails. A package whose engines do not match the host
Node is blocked, not a failed apply. A mise or asdf shim is left alone
(managed by mise or asdf).

The result is also written to $XDG_CACHE_HOME/clifleet/last-report.json
(or ~/.cache/clifleet/last-report.json), and every run that is not
--dry-run is appended to history.jsonl next to it (clifleet history).
With a pipe, use set -o pipefail so tee does not hide a failed upgrade.

--dry-run still connects, so the printed command matches the install
method on disk, but it does not run the upgrade.

Before fetching, clifleet asks (from this machine, once per tool) for the
newest version on the channel fetch would download from. A tool whose
installed version already equals it is "up to date" and is not
downloaded. If the lookup fails, the tool is fetched as before. --apply
never skips on this check; it installs what fetch staged. --no-latest
turns the check off.

Flags:
  -c, --config PATH
      --hosts LIST
      --only LIST
      --timeout DURATION
      --parallel N
      --fetch            Stage artifacts; do not replace binaries
      --apply            Install from the staged cache
      --one-shot         Run native updaters in place instead of fetch-then-swap
      --no-latest        Fetch every stageable tool without checking the newest version
      --dry-run
      --json
  -v, --verbose
  -h, --help

Examples:
  clifleet upgrade
  clifleet upgrade --dry-run
  clifleet upgrade --fetch --only codex
  clifleet upgrade --apply
  clifleet upgrade --one-shot --only agent
  clifleet upgrade --hosts us-server,workshop
`

const noStageTip = "Tip: tools marked no-stage have no staged artifact. clifleet upgrade --one-shot runs their updater in place."

const doctorHelp = `Usage:
  clifleet doctor [flags]

Check the local OpenSSH client, the fleet file, identity files, and
(unless --offline) whether each host accepts a BatchMode SSH session.
Host key checking is left to your ssh config. clifleet does not turn it off.

Flags:
  -c, --config PATH
      --hosts LIST
      --timeout DURATION   Per host (default 10s)
      --offline            Do not connect
  -h, --help

Examples:
  clifleet doctor
  clifleet doctor --offline
`

type options struct {
	config   string
	hosts    []string
	only     []string
	timeout  string
	parallel int
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			*s = append(*s, p)
		}
	}
	return nil
}

func parseFlags(args []string, stderr io.Writer, help string, bind func(*flag.FlagSet)) (*options, *flag.FlagSet, error) {
	fs := flag.NewFlagSet("clifleet", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, help) }
	opt := &options{}
	fs.StringVar(&opt.config, "config", "", "fleet file")
	fs.StringVar(&opt.config, "c", "", "fleet file")
	var hosts, only stringList
	fs.Var(&hosts, "hosts", "comma-separated hosts")
	fs.Var(&only, "only", "comma-separated tools")
	fs.StringVar(&opt.timeout, "timeout", "", "timeout")
	fs.IntVar(&opt.parallel, "parallel", 0, "concurrency")
	if bind != nil {
		bind(fs)
	}
	if err := fs.Parse(args); err != nil {
		return nil, fs, err
	}
	opt.hosts = append([]string(nil), hosts...)
	opt.only = append([]string(nil), only...)
	return opt, fs, nil
}

func cmdInit(args []string, stdout, stderr io.Writer) int {
	var force bool
	opt, fs, err := parseFlags(args, stderr, initHelp, func(fs *flag.FlagSet) {
		fs.BoolVar(&force, "force", false, "replace an existing fleet file")
	})
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "clifleet init: unexpected %s\n", strings.Join(fs.Args(), " "))
		return 1
	}
	path, err := resolveConfig(opt.config)
	if err != nil {
		fmt.Fprintf(stderr, "clifleet: %s\n", err)
		return 1
	}
	if _, err := os.Stat(path); err == nil && !force {
		fmt.Fprintf(stderr, "clifleet: %s already exists (use --force to replace it)\n", path)
		return 1
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Fprintf(stderr, "clifleet: %s\n", err)
		return 1
	}
	if err := os.WriteFile(path, []byte(config.Example), 0o644); err != nil {
		fmt.Fprintf(stderr, "clifleet: %s\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Wrote %s\n\nNext:\n  edit the hosts so they match your fleet\n  clifleet doctor\n  clifleet status\n  clifleet upgrade --dry-run\n", path)
	return 0
}

func cmdFleet(args []string, stdout, stderr io.Writer, upgrade bool) int {
	help := statusHelp
	name := "status"
	if upgrade {
		help = upgradeHelp
		name = "upgrade"
	}
	var dry, jsonOut, verbose, fetch, apply, oneShot, noLatest bool
	opt, fs, err := parseFlags(args, stderr, help, func(fs *flag.FlagSet) {
		fs.BoolVar(&dry, "dry-run", false, "do not change hosts")
		fs.BoolVar(&noLatest, "no-latest", false, "do not look up the newest versions")
		fs.BoolVar(&jsonOut, "json", false, "print JSON")
		fs.BoolVar(&verbose, "verbose", false, "show remote output")
		fs.BoolVar(&verbose, "v", false, "show remote output")
		if upgrade {
			fs.BoolVar(&fetch, "fetch", false, "stage artifacts, do not replace binaries")
			fs.BoolVar(&apply, "apply", false, "install from the staged cache")
			fs.BoolVar(&oneShot, "one-shot", false, "run native updaters in place")
		}
	})
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "clifleet %s: unexpected %s\n", name, strings.Join(fs.Args(), " "))
		return 1
	}
	if fetch && apply {
		fmt.Fprintf(stderr, "clifleet upgrade: --fetch and --apply are separate steps\n")
		return 1
	}
	if oneShot && (fetch || apply) {
		fmt.Fprintf(stderr, "clifleet upgrade: --one-shot cannot be combined with --fetch or --apply\n")
		return 1
	}
	phase := fleet.PhaseUpgrade
	if fetch {
		phase = fleet.PhaseFetch
	}
	if apply {
		phase = fleet.PhaseApply
	}
	cfg, code := loadFleet(opt, stderr)
	if code != 0 {
		return code
	}
	timeout, parallel, code := timing(opt, stderr)
	if code != 0 {
		return code
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	notice := func() string { return "" }
	var progress func(string)
	if isTerm(stderr) && !jsonOut {
		if !dry {
			notice = startUpdateNotice(ctx)
		}
		var mu sync.Mutex
		progress = func(line string) {
			mu.Lock()
			fmt.Fprintln(stderr, line)
			mu.Unlock()
		}
	}
	fopt := fleet.Options{
		Hosts: opt.hosts, Only: opt.only, DryRun: dry,
		Parallel: parallel, Timeout: timeout, Verbose: verbose,
		Phase: phase, OneShot: oneShot, OnProgress: progress,
	}
	if !noLatest {
		fopt.Latest = latest.NewResolver(latestTimeout).Latest
	}
	var report fleet.Report
	if upgrade {
		report, err = fleet.Upgrade(ctx, cfg, fopt, runner.Exec{})
	} else {
		report, err = fleet.Status(ctx, cfg, fopt, runner.Exec{})
	}
	if err != nil {
		fmt.Fprintf(stderr, "clifleet: %s\n", err)
		return 1
	}
	if werr := writeLastReport(report); werr != nil {
		fmt.Fprintf(stderr, "clifleet: could not write last report: %s\n", werr)
	}
	if herr := appendHistory(report, time.Now()); herr != nil {
		fmt.Fprintf(stderr, "clifleet: could not append history: %s\n", herr)
	}
	if jsonOut {
		raw, jerr := report.JSON()
		if jerr != nil {
			fmt.Fprintf(stderr, "clifleet: %s\n", jerr)
			return 1
		}
		fmt.Fprintf(stdout, "%s\n", raw)
		if report.Failed() {
			return 2
		}
		return 0
	}
	lines := report.Lines()
	if len(lines) == 0 {
		fmt.Fprintln(stdout, "no tools to check on the selected hosts")
	} else {
		fmt.Fprintln(stdout, table.Render(report.Header(), lines, colorEnabled(stdout)))
	}
	fmt.Fprintln(stdout, report.Summary())
	if upgrade && phase == fleet.PhaseUpgrade && !oneShot && reportHasNoStage(report) {
		fmt.Fprintln(stderr, noStageTip)
	}
	for _, e := range report.Errors() {
		fmt.Fprintf(stderr, "%s\n", e)
	}
	if verbose {
		for _, h := range report.Hosts {
			if strings.TrimSpace(h.Log) == "" {
				continue
			}
			fmt.Fprintf(stderr, "\n# %s\n%s\n", h.Name, strings.TrimSpace(h.Log))
		}
	}
	if n := notice(); n != "" {
		fmt.Fprintln(stderr, n)
	}
	if report.Failed() {
		return 2
	}
	return 0
}

// writeLastReport leaves the JSON report at
// $XDG_CACHE_HOME/clifleet/last-report.json, or ~/.cache/clifleet when
// XDG_CACHE_HOME is unset. A long run piped through tee can drop the table;
// this file is still there when the process exits.
func writeLastReport(report fleet.Report) error {
	path, err := cachePath("last-report.json")
	if err != nil {
		return err
	}
	raw, err := report.JSON()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// A temp name of its own, so two runs finishing together cannot rename
	// each other's half-written file into place.
	tmp, err := os.CreateTemp(filepath.Dir(path), "last-report-*.tmp")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(append(raw, '\n'))
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp.Name(), 0o644)
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), path)
	}
	if werr != nil {
		os.Remove(tmp.Name())
	}
	return werr
}

// cachePath is name under $XDG_CACHE_HOME/clifleet, or ~/.cache/clifleet.
func cachePath(name string) (string, error) {
	if dir := strings.TrimSpace(os.Getenv("XDG_CACHE_HOME")); dir != "" {
		return filepath.Join(dir, "clifleet", name), nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("no home directory for the report cache")
	}
	return filepath.Join(home, ".cache", "clifleet", name), nil
}

func reportHasNoStage(report fleet.Report) bool {
	for _, h := range report.Hosts {
		for _, row := range h.Rows {
			if row.Status == fleet.StNoStage {
				return true
			}
		}
	}
	return false
}

func cmdDoctor(args []string, stdout, stderr io.Writer) int {
	var offline bool
	opt, fs, err := parseFlags(args, stderr, doctorHelp, func(fs *flag.FlagSet) {
		fs.BoolVar(&offline, "offline", false, "do not connect")
	})
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "clifleet doctor: unexpected %s\n", strings.Join(fs.Args(), " "))
		return 1
	}
	cfg, code := loadFleet(opt, stderr)
	if code != 0 {
		return code
	}
	hosts, err := config.FilterHosts(cfg.Hosts, opt.hosts)
	if err != nil {
		fmt.Fprintf(stderr, "clifleet: %s\n", err)
		return 1
	}
	timeout := 10 * time.Second
	if opt.timeout != "" {
		d, perr := config.ParseDuration(opt.timeout)
		if perr != nil {
			fmt.Fprintf(stderr, "clifleet: timeout: %s\n", perr)
			return 1
		}
		timeout = d
	}

	type check struct{ name, status, detail string }
	rows := []check{{"config", "ok", fmt.Sprintf("%s (%d hosts)", cfg.Path, len(cfg.Hosts))}}

	_, lookErr := exec.LookPath("ssh")
	if lookErr != nil {
		rows = append(rows, check{"ssh", "fail", "ssh not found on PATH"})
	} else if ver := sshVersion(); ver != "" {
		rows = append(rows, check{"ssh", "ok", ver})
	} else {
		rows = append(rows, check{"ssh", "ok", "found"})
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	failed := lookErr != nil
	for _, h := range hosts {
		if h.Identity != "" {
			if _, err := os.Stat(h.Identity); err != nil {
				rows = append(rows, check{h.Name, "fail", "identity not found: " + h.Identity})
				failed = true
				continue
			}
		}
		if h.Local {
			rows = append(rows, check{h.Name, "ok", "local"})
			continue
		}
		dest, port, err := config.Destination(h)
		if err != nil {
			rows = append(rows, check{h.Name, "fail", err.Error()})
			failed = true
			continue
		}
		detail := dest
		if port > 0 {
			detail = fmt.Sprintf("%s port %d", dest, port)
		}
		if offline || lookErr != nil {
			rows = append(rows, check{h.Name, "ok", "not connected (" + detail + ")"})
			continue
		}
		t := runner.Target{Name: h.Name, Destination: dest, Port: port, Identity: h.Identity, Timeout: timeout}
		start := time.Now()
		// ConnectTimeout only covers the TCP connect. A host that hangs after
		// that (auth, a stuck shell) must not stall doctor.
		hctx, cancel := context.WithTimeout(ctx, timeout+doctorGrace)
		cmd := exec.CommandContext(hctx, "ssh", append(runner.DialArgs(t), "true")...)
		cmd.WaitDelay = time.Second
		out, err := cmd.CombinedOutput()
		timedOut := hctx.Err() == context.DeadlineExceeded
		cancel()
		if err != nil {
			msg := firstLine(string(out))
			if timedOut {
				msg = fmt.Sprintf("timed out after %s", (timeout + doctorGrace).Round(time.Second))
			} else if msg == "" {
				msg = err.Error()
			}
			rows = append(rows, check{h.Name, "fail", msg})
			failed = true
			continue
		}
		rows = append(rows, check{h.Name, "ok", fmt.Sprintf("%s (%s)", dest, time.Since(start).Round(time.Millisecond))})
	}

	lines := make([][]string, len(rows))
	for i, r := range rows {
		lines[i] = []string{r.name, r.status, r.detail}
	}
	fmt.Fprintln(stdout, table.Render([]string{"CHECK", "RESULT", "DETAIL"}, lines, colorEnabled(stdout)))
	if failed {
		return 2
	}
	return 0
}

// latestTimeout caps each newest-version lookup.
const latestTimeout = 10 * time.Second

// doctorGrace is how long doctor waits past the connect timeout for ssh to
// log in and run `true`.
var doctorGrace = 5 * time.Second

func sshVersion() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", "-V")
	cmd.WaitDelay = time.Second
	out, _ := cmd.CombinedOutput()
	line := firstLine(string(out))
	if i := strings.Index(line, ","); i > 0 {
		line = strings.TrimSpace(line[:i])
	}
	return line
}

func loadFleet(opt *options, stderr io.Writer) (config.Loaded, int) {
	path, err := resolveConfig(opt.config)
	if err != nil {
		fmt.Fprintf(stderr, "clifleet: %s\n", err)
		return config.Loaded{}, 1
	}
	cfg, err := config.Load(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(stderr, "clifleet: no fleet file at %s\nRun clifleet init to create one.\n", path)
			return config.Loaded{}, 1
		}
		fmt.Fprintf(stderr, "clifleet: %s\n", err)
		return config.Loaded{}, 1
	}
	return cfg, 0
}

func resolveConfig(explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return config.ExpandHome(explicit)
	}
	return config.DefaultPath()
}

func timing(opt *options, stderr io.Writer) (time.Duration, int, int) {
	var timeout time.Duration
	if opt.timeout != "" {
		d, err := config.ParseDuration(opt.timeout)
		if err != nil {
			fmt.Fprintf(stderr, "clifleet: timeout: %s\n", err)
			return 0, 0, 1
		}
		if d < config.MinTimeout || d > config.MaxTimeout {
			fmt.Fprintf(stderr, "clifleet: timeout must be between %s and %s\n", config.MinTimeout, config.MaxTimeout)
			return 0, 0, 1
		}
		timeout = d
	}
	if opt.parallel < 0 || opt.parallel > config.MaxConcurrency {
		fmt.Fprintf(stderr, "clifleet: parallel must be 1..%d\n", config.MaxConcurrency)
		return 0, 0, 1
	}
	return timeout, opt.parallel, 0
}

func colorEnabled(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	return isTerm(w)
}

func isTerm(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
