// Package probe builds the remote shell script and parses its stdout.
// The probe script only reads versions. Upgrade commands are a separate
// script, and only for tools the probe already found.
package probe

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/indiekitai/clifleet/internal/catalog"
	"github.com/indiekitai/clifleet/internal/shellquote"
)

// Tool is a candidate binary to look up on a host.
type Tool struct {
	Name   string
	Binary string
}

// scriptPrelude is POSIX sh. It must not install, upgrade, or download.
const scriptPrelude = `# clifleet remote helper. POSIX sh.
# Reads versions and, when asked, runs an upgrade argv chosen on the client.
# stdin of every child is /dev/null so a CLI cannot consume this script.
set -f
unset CDPATH
PATH=${PATH:-/usr/bin:/bin}
export PATH

prepend() {
  d=$1
  if [ -n "$d" ] && [ -d "$d" ]; then
    PATH="$d:$PATH"
  fi
}

# append_missing adds a directory that is not already on PATH. Used after nvm,
# which sometimes replaces PATH with a snapshot that dropped Homebrew.
append_missing() {
  d=$1
  if [ -z "$d" ] || [ ! -d "$d" ]; then
    return 0
  fi
  case ":$PATH:" in
    *":$d:"*) return 0 ;;
  esac
  PATH="$PATH:$d"
}

# Printed before any slow work so a timeout can tell "script never started"
# from "script started and stalled". doctor only runs true, so it can succeed
# while this script is still sitting in a shell startup or a version check.
printf '%s\n' 'clifleet-script/1' >&2
`

// loginPathDirs are the directories a login shell would already have searched.
// Homebrew is last so prepend puts /opt/homebrew/bin ahead of /usr/local/bin.
// Entries are shell words, not remote input.
var loginPathDirs = []string{
	`"$HOME/.local/bin"`,
	`"$HOME/.cargo/bin"`,
	`"$HOME/.npm-global/bin"`,
	`"$HOME/.volta/bin"`,
	`"$HOME/bin"`,
	`"$HOME/.local/share/mise/shims"`,
	`"$HOME/.linuxbrew/sbin"`,
	`"$HOME/.linuxbrew/bin"`,
	`/home/linuxbrew/.linuxbrew/sbin`,
	`/home/linuxbrew/.linuxbrew/bin`,
	`/usr/local/sbin`,
	`/usr/local/bin`,
	`/opt/homebrew/sbin`,
	`/opt/homebrew/bin`,
}

const scriptFns = `
# cf_cap SECONDS OUTFILE CMD... runs CMD with stdin /dev/null and kills it
# when SECONDS elapses. A stuck --version or nvm.sh must not burn the whole
# SSH budget. Exit 124 means the cap fired. Fast commands return immediately.
cf_cap() {
  secs=$1
  outfile=$2
  shift 2
  : >"$outfile"
  flag="$outfile.t"
  rm -f "$flag"
  "$@" </dev/null >"$outfile" 2>/dev/null &
  pid=$!
  (
    sleep "$secs"
    if kill -0 "$pid" 2>/dev/null; then
      printf '1\n' >"$flag"
      kill "$pid" 2>/dev/null || true
      sleep 1
      kill -9 "$pid" 2>/dev/null || true
    fi
  ) </dev/null >/dev/null 2>&1 &
  kpid=$!
  status=0
  wait "$pid" 2>/dev/null || status=$?
  kill "$kpid" 2>/dev/null || true
  wait "$kpid" 2>/dev/null || true
  if [ -f "$flag" ]; then
    return 124
  fi
  return "$status"
}

# cf_limit SECONDS CMD... runs CMD with stdin /dev/null and kills it when
# SECONDS elapses. Unlike cf_cap, stdout is left attached so CF1 lines still
# reach the script. Exit 124 means the limit fired.
cf_limit() {
  secs=$1
  shift
  flag=$cf_dir/limit.$$
  rm -f "$flag"
  "$@" </dev/null &
  pid=$!
  (
    sleep "$secs"
    if kill -0 "$pid" 2>/dev/null; then
      printf '1\n' >"$flag"
      kill "$pid" 2>/dev/null || true
      sleep 1
      kill -9 "$pid" 2>/dev/null || true
    fi
  ) </dev/null >/dev/null 2>&1 &
  kpid=$!
  status=0
  wait "$pid" 2>/dev/null || status=$?
  kill "$kpid" 2>/dev/null || true
  wait "$kpid" 2>/dev/null || true
  if [ -f "$flag" ]; then
    return 124
  fi
  return "$status"
}

resolve() {
  target=$1
  i=0
  while [ -L "$target" ] && [ "$i" -lt 20 ]; do
    link=$(readlink "$target" 2>/dev/null) || break
    case $link in
      /*) target=$link ;;
      *) target=$(dirname "$target")/$link ;;
    esac
    i=$((i + 1))
  done
  printf '%s\n' "$target"
}

brew_meta() {
  p=$1
  kind=
  rest=
  case $p in
    */Caskroom/*)
      kind=cask
      rest=${p#*/Caskroom/}
      ;;
    */Cellar/*)
      kind=formula
      rest=${p#*/Cellar/}
      ;;
    *)
      return 0
      ;;
  esac
  token=${rest%%/*}
  printf '%s|%s\n' "$kind" "$token"
}

pkg_name() {
  dir=$(dirname "$1")
  i=0
  while [ "$i" -lt 8 ]; do
    if [ -f "$dir/package.json" ]; then
      sed -n 's/^[[:space:]]*"name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$dir/package.json" | head -n 1
      return 0
    fi
    parent=$(dirname "$dir")
    if [ "$parent" = "$dir" ]; then
      return 0
    fi
    dir=$parent
    i=$((i + 1))
  done
}

flatten() {
  printf '%s' "$1" | tr '\t\r\n' '   ' | cut -c1-200
}

probe_one() {
  label=$1
  binary=$2
  path=$(command -v "$binary" 2>/dev/null || true)
  if [ -z "$path" ]; then
    printf 'CF1\tmissing\t%s\n' "$label"
    return 0
  fi
  real=$(resolve "$path")
  if [ -z "$real" ]; then
    real=$path
  fi
  verfile=$cf_dir/ver
  if cf_cap 3 "$verfile" "$path" --version; then
    ver=$(head -n 1 "$verfile" 2>/dev/null || true)
  else
    rc=$?
    if [ "$rc" -eq 124 ]; then
      ver="version timed out"
    else
      ver=$(head -n 1 "$verfile" 2>/dev/null || true)
    fi
  fi
  ver=$(flatten "$ver")
  meta=$(brew_meta "$real")
  if [ -z "$meta" ]; then
    meta=$(brew_meta "$path")
  fi
  kind=
  token=
  if [ -n "$meta" ]; then
    kind=${meta%%|*}
    token=${meta#*|}
  fi
  pkg=$(pkg_name "$real")
  printf 'CF1\tpresent\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$label" "$path" "$real" "$ver" "$kind" "$token" "$pkg"
}
`

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)

// ProbeScript looks up each tool and prints a parseable table. It does not
// change the machine.
func ProbeScript(tools []Tool, extraPaths []string) (string, error) {
	body, err := scriptBody(extraPaths)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(body)
	b.WriteString("printf '%s\\n' 'clifleet-probe/1'\n")
	if err := writeProbes(&b, tools); err != nil {
		return "", err
	}
	b.WriteString("exit 0\n")
	return b.String(), nil
}

// Step is one upgrade to run on a host, labeled by tool name.
type Step struct {
	Tool string
	Cmd  catalog.Command
}

// UpgradeScriptSteps builds the upgrade script.
func UpgradeScriptSteps(reprobe []Tool, extraPaths []string, steps []Step) (string, error) {
	if len(steps) == 0 {
		return "", fmt.Errorf("no upgrade commands")
	}
	body, err := scriptBody(extraPaths)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(body)
	b.WriteString(`export NONINTERACTIVE=1
export HOMEBREW_NO_AUTO_UPDATE=1
export HOMEBREW_NO_ENV_HINTS=1
export HOMEBREW_NO_ANALYTICS=1
export npm_config_fund=false
export npm_config_audit=false
export npm_config_update_notifier=false
printf '%s\n' 'clifleet-upgrade/1'
`)
	for _, step := range steps {
		if !nameRe.MatchString(step.Tool) {
			return "", fmt.Errorf("invalid tool name %q", step.Tool)
		}
		if len(step.Cmd.Argv) == 0 {
			return "", fmt.Errorf("empty upgrade command for %s", step.Tool)
		}
		for _, a := range step.Cmd.Argv {
			if strings.ContainsAny(a, "\n\r") {
				return "", fmt.Errorf("refusing upgrade arg with newline for %s", step.Tool)
			}
		}
		// stdout joins stderr so CF1 lines stay the only stdout.
		fmt.Fprintf(&b, "rc=0\n%s </dev/null >&2 || rc=$?\n", shellquote.Format(step.Cmd.Argv))
		fmt.Fprintf(&b, "printf 'CF1\\tupgrade\\t%%s\\t%%s\\n' %s \"$rc\"\n", shellquote.Quote(step.Tool))
	}
	b.WriteString("printf '%s\\n' 'clifleet-probe/1'\n")
	if err := writeProbes(&b, reprobe); err != nil {
		return "", err
	}
	b.WriteString("exit 0\n")
	return b.String(), nil
}

func scriptBody(extraPaths []string) (string, error) {
	var b strings.Builder
	b.WriteString(scriptPrelude)
	if err := writePathFn(&b, "cf_login_path", "prepend", extraPaths); err != nil {
		return "", err
	}
	if err := writePathFn(&b, "cf_ensure_path", "append_missing", extraPaths); err != nil {
		return "", err
	}
	b.WriteString(scriptFns)
	b.WriteString(brewEnvSnippet)
	b.WriteString(nvmSnippet)
	return b.String(), nil
}

func writePathFn(b *strings.Builder, name, helper string, extra []string) error {
	fmt.Fprintf(b, "%s() {\n", name)
	for _, dir := range loginPathDirs {
		fmt.Fprintf(b, "  %s %s\n", helper, dir)
	}
	for _, p := range extra {
		if !validExtra(p) {
			return fmt.Errorf("invalid path_extra %q", p)
		}
		fmt.Fprintf(b, "  %s %s\n", helper, shellquote.Quote(p))
	}
	b.WriteString("}\n")
	return nil
}

// brewEnvSnippet is the non-interactive equivalent of a login shell's
// eval "$(brew shellenv)". Absolute paths are checked first because ssh -T
// starts with PATH=/usr/bin:/bin:/usr/sbin:/sbin. Output is capped so a stuck
// brew cannot hold the probe. This is not bash -lc.
const brewEnvSnippet = `
cf_brew_env() {
  brewbin=
  if [ -x /opt/homebrew/bin/brew ]; then
    brewbin=/opt/homebrew/bin/brew
  elif [ -x /usr/local/bin/brew ]; then
    brewbin=/usr/local/bin/brew
  elif [ -x /home/linuxbrew/.linuxbrew/bin/brew ]; then
    brewbin=/home/linuxbrew/.linuxbrew/bin/brew
  elif command -v brew >/dev/null 2>&1; then
    brewbin=$(command -v brew)
  fi
  if [ -z "$brewbin" ]; then
    return 0
  fi
  envfile=$cf_dir/brew.env
  if cf_cap 5 "$envfile" "$brewbin" shellenv; then
    eval "$(cat "$envfile")"
  fi
}
`

// nvmSnippet loads nvm's PATH from a capped child so nvm.sh cannot read this
// script's stdin (sh -s) and cannot hang the session. The child prints PATH.
// cf_ensure_path runs again afterwards: nvm sometimes replaces PATH and drops
// Homebrew, which is how a non-interactive ssh loses brew.
const nvmSnippet = `
export PATH
cf_dir=${TMPDIR:-/tmp}/clifleet.$$
mkdir -p "$cf_dir" || {
  printf '%s\n' 'clifleet: cannot create temp dir' >&2
  exit 1
}
trap 'rm -rf "$cf_dir"' EXIT
trap 'rm -rf "$cf_dir"; exit 143' INT TERM
cf_login_path
cf_brew_env
if [ -n "${HOME:-}" ] && [ -s "$HOME/.nvm/nvm.sh" ]; then
  nvm_out=$cf_dir/nvm.path
  if cf_cap 5 "$nvm_out" sh -c '. "$HOME/.nvm/nvm.sh" >/dev/null 2>&1; printf %s "$PATH"'; then
    nvm_path=$(cat "$nvm_out" 2>/dev/null || true)
    if [ -n "$nvm_path" ]; then
      PATH="$nvm_path"
    fi
  fi
fi
cf_ensure_path
cf_brew_env
hash -r 2>/dev/null || true
export PATH
`

func writeProbes(b *strings.Builder, tools []Tool) error {
	if len(tools) == 0 {
		return fmt.Errorf("no tools to probe")
	}
	for _, t := range tools {
		if !nameRe.MatchString(t.Name) {
			return fmt.Errorf("invalid tool name %q", t.Name)
		}
		binary := t.Binary
		if binary == "" {
			binary = t.Name
		}
		if !nameRe.MatchString(binary) {
			return fmt.Errorf("invalid binary name %q", binary)
		}
		fmt.Fprintf(b, "probe_one %s %s\n", shellquote.Quote(t.Name), shellquote.Quote(binary))
	}
	return nil
}

func validExtra(p string) bool {
	if len(p) == 0 || len(p) > 256 || !strings.HasPrefix(p, "/") || strings.Contains(p, "..") {
		return false
	}
	for _, r := range p {
		if r > unicode.MaxASCII || !(unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._@+/-", r)) {
			return false
		}
	}
	return true
}

// ParseProbe reads a probe script's stdout.
func ParseProbe(stdout string) (map[string]catalog.Observation, error) {
	if !strings.Contains(stdout, "clifleet-probe/1") {
		return nil, fmt.Errorf("remote probe did not return a clifleet result")
	}
	obs := map[string]catalog.Observation{}
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimRight(line, "\r")
		if !strings.HasPrefix(line, "CF1\t") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 3 {
			return nil, fmt.Errorf("short probe line %q", line)
		}
		switch fields[1] {
		case "missing":
			name := fields[2]
			if _, ok := obs[name]; ok {
				return nil, fmt.Errorf("duplicate probe result for %s", name)
			}
			obs[name] = catalog.Observation{Tool: name, Present: false}
		case "present":
			if len(fields) < 9 {
				return nil, fmt.Errorf("short present line %q", line)
			}
			name := fields[2]
			if _, ok := obs[name]; ok {
				return nil, fmt.Errorf("duplicate probe result for %s", name)
			}
			obs[name] = catalog.Observation{
				Tool:      name,
				Present:   true,
				Path:      fields[3],
				Real:      fields[4],
				Version:   strings.TrimSpace(fields[5]),
				BrewKind:  fields[6],
				BrewToken: fields[7],
				Package:   fields[8],
			}
		case "upgrade", "fetch", "apply":
			continue
		default:
			return nil, fmt.Errorf("unknown probe tag %q", fields[1])
		}
	}
	return obs, nil
}

// UpgradeResult is the remote exit status of one upgrade command.
type UpgradeResult struct {
	Tool string
	Exit int
}

// ParseUpgrade reads an upgrade script's stdout: command statuses plus a
// fresh probe.
func ParseUpgrade(stdout string) ([]UpgradeResult, map[string]catalog.Observation, error) {
	if !strings.Contains(stdout, "clifleet-upgrade/1") {
		return nil, nil, fmt.Errorf("remote upgrade did not return a clifleet result")
	}
	var results []UpgradeResult
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimRight(line, "\r")
		if !strings.HasPrefix(line, "CF1\tupgrade\t") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 4 {
			return nil, nil, fmt.Errorf("short upgrade line %q", line)
		}
		code, err := strconv.Atoi(strings.TrimSpace(fields[3]))
		if err != nil {
			return nil, nil, fmt.Errorf("bad exit code %q", fields[3])
		}
		results = append(results, UpgradeResult{Tool: fields[2], Exit: code})
	}
	obs, err := ParseProbe(stdout)
	if err != nil {
		return results, nil, err
	}
	return results, obs, nil
}

var semverRe = regexp.MustCompile(`\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?`)

// ShortVersion pulls a semver-looking token out of a raw --version line.
func ShortVersion(raw string) string {
	raw = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, raw)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if m := semverRe.FindString(raw); m != "" {
		return m
	}
	if len(raw) > 32 {
		return raw[:32]
	}
	return raw
}
