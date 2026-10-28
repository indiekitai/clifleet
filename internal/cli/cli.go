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
	"github.com/indiekitai/clifleet/internal/runner"
	"github.com/indiekitai/clifleet/internal/table"
)

// Version is the released version, overridable with -ldflags.
var Version = "0.1.0"

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
		fmt.Fprintf(stdout, "clifleet %s\n", Version)
		return 0
	case "init":
		return cmdInit(args[1:], stdout, stderr)
	case "status":
		return cmdFleet(args[1:], stdout, stderr, false)
	case "upgrade":
		return cmdFleet(args[1:], stdout, stderr, true)
	case "doctor":
		return cmdDoctor(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "clifleet: unknown command %q\n\n%s", args[0], rootHelp)
		return 1
	}
}

const rootHelp = `clifleet — Upgrade your AI CLIs across every machine

Usage:
  clifleet <command> [flags]

Commands:
  init       Write an example fleet file
  status     Show host × tool × version × install method
  upgrade    Upgrade CLIs that are already installed
  doctor     Check SSH, the fleet file, and reachability
  version    Print the clifleet version

clifleet looks at each machine separately. A tool that is not installed
there is skipped. Nothing in the candidate list is installed for you,
and a host allowlist or blocklist cannot be widened from the command line.

Examples:
  clifleet init
  clifleet status
  clifleet status --dry-run
  clifleet upgrade --dry-run
  clifleet upgrade --only claude --hosts us-server
  clifleet doctor

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

--dry-run does not open connections. It prints the tools clifleet would
look for after allowlists, blocklists, and --only.

--only narrows the candidate list. It cannot add a tool that a host
allowlist or blocklist already excluded.

Flags:
  -c, --config PATH      Fleet file (default ~/.config/clifleet/hosts.toml)
      --hosts LIST       Comma-separated host names or SSH aliases
      --only LIST        Comma-separated tools to look for
      --timeout DURATION Per SSH session (default from the fleet file, else 3m)
      --parallel N       Concurrent sessions (default 8, max 64)
      --dry-run          Print the probe plan; do not connect
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

On each host, upgrade the candidate CLIs that are already installed,
using that host's install method:

  native Claude Code     claude update
  native Codex           codex update
  native OpenCode        opencode upgrade
  npm                    npm install -g <package>@latest
  Homebrew cask          brew upgrade --cask <token>
  Homebrew formula       brew upgrade <token>

The built-in catalog is wider than those three. Each tool uses the channel
that matches the binary on disk. A tool with no recipe for that channel is
skipped. Opt-in names (dsh, grok, kilo, kiro-cli) are not searched unless
the fleet file lists them.

A missing CLI is skipped. clifleet will not brew install, npm install a
tool that is not already on PATH, or curl an installer. Commands run
without a TTY (stdin is /dev/null) and with non-interactive brew and npm
settings. The exit code is non-zero if any host cannot be reached or any
upgrade command fails.

--dry-run still connects, so the printed command matches the install
method on disk, but it does not run the upgrade.

Flags:
  -c, --config PATH
      --hosts LIST
      --only LIST
      --timeout DURATION
      --parallel N
      --dry-run
      --json
  -v, --verbose
  -h, --help

Examples:
  clifleet upgrade --dry-run
  clifleet upgrade --only claude
  clifleet upgrade --hosts us-server,workshop
`

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
	var dry, jsonOut, verbose bool
	opt, fs, err := parseFlags(args, stderr, help, func(fs *flag.FlagSet) {
		fs.BoolVar(&dry, "dry-run", false, "do not change hosts")
		fs.BoolVar(&jsonOut, "json", false, "print JSON")
		fs.BoolVar(&verbose, "verbose", false, "show remote output")
		fs.BoolVar(&verbose, "v", false, "show remote output")
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

	var progress func(string)
	if isTerm(stderr) && !jsonOut {
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
		OnProgress: progress,
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
		fmt.Fprintln(stdout, table.Render([]string{"HOST", "TOOL", "VERSION", "METHOD", "STATUS"}, lines, colorEnabled(stdout)))
	}
	fmt.Fprintln(stdout, report.Summary())
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
	if report.Failed() {
		return 2
	}
	return 0
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
		cmd := exec.CommandContext(ctx, "ssh", append(runner.DialArgs(t), "true")...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			msg := firstLine(string(out))
			if msg == "" {
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

func sshVersion() string {
	cmd := exec.Command("ssh", "-V")
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
