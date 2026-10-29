package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/indiekitai/clifleet/internal/selfupdate"
)

const selfUpdateHelp = `Usage:
  clifleet update [--check] [--force]

Replace this clifleet binary with the latest GitHub release build. Only
clifleet itself is touched; the CLIs on your hosts are not (that is
clifleet upgrade). self-update is the same command. The archive
must match the release's SHA256SUMS; when gh is installed and logged in,
gh attestation verify must also confirm this repo's release workflow built
it (CLIFLEET_NO_ATTESTATION=1 skips that). The new binary must run here
before it replaces the old one. A clifleet built from source (version
"dev") is left alone unless --force is set.

In a terminal, status and upgrade (not --dry-run) end with a one-line hint when a newer
release exists. That check runs beside the fleet run, at most once a day
(once an hour after a failure), and CLIFLEET_NO_UPDATE_CHECK=1 turns it off.

To install clifleet without Go:

  curl -fsSL https://raw.githubusercontent.com/indiekitai/clifleet/main/install.sh | sh

Flags:
      --check   Print the current and latest version; change nothing
      --force   Reinstall even when this version is current, or replace a source build
  -h, --help
`

// selfPath is the binary update replaces. Tests point it at a temp file.
var selfPath = func() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}

// version is the ldflags stamp of a release build, else the module version
// go install recorded (a tag or a pseudo-version), else dev.
func version() string {
	if Version != "" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

func cmdSelfUpdate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("clifleet update", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, selfUpdateHelp) }
	var check, force bool
	fs.BoolVar(&check, "check", false, "report only")
	fs.BoolVar(&force, "force", false, "reinstall")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "clifleet update: unexpected %s\n", strings.Join(fs.Args(), " "))
		return 1
	}
	target, err := selfPath()
	if err != nil {
		fmt.Fprintf(stderr, "clifleet update: cannot find this binary: %s\n", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cur := version()
	res, err := selfupdate.Update(ctx, selfupdate.Options{
		Current: cur, Target: target, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		Check: check, Force: force,
		NoAttestation: os.Getenv("CLIFLEET_NO_ATTESTATION") != "",
	})
	if err != nil {
		fmt.Fprintf(stderr, "clifleet update: %s\n", err)
		return 2
	}
	switch {
	case res.Updated:
		checked := "SHA256SUMS"
		if res.Attested {
			checked += " and build attestation"
		}
		fmt.Fprintf(stdout, "clifleet %s → %s (%s; checked %s)\n", cur, res.Latest, target, checked)
	case !res.Available:
		fmt.Fprintf(stdout, "clifleet %s is current (latest release %s)\n", cur, res.Latest)
	default:
		fmt.Fprintf(stdout, "clifleet %s; latest release is %s. Run clifleet update to install it.\n", cur, res.Latest)
	}
	return 0
}
