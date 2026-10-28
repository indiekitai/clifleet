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

prepend() {
  d=$1
  if [ -n "$d" ] && [ -d "$d" ]; then
    PATH="$d:$PATH"
  fi
}

prepend "$HOME/.local/bin"
prepend "$HOME/.cargo/bin"
prepend "$HOME/.npm-global/bin"
prepend "$HOME/.volta/bin"
prepend "$HOME/bin"
prepend "$HOME/.local/share/mise/shims"
prepend /opt/homebrew/bin
prepend /usr/local/bin
prepend /home/linuxbrew/.linuxbrew/bin

# nvm is sourced for PATH only. A failure here must not abort the probe.
if [ -n "${HOME:-}" ] && [ -s "$HOME/.nvm/nvm.sh" ]; then
  . "$HOME/.nvm/nvm.sh" >/dev/null 2>&1 || true
fi
`

const scriptFns = `
hash -r 2>/dev/null || true
export PATH

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
  ver=$("$path" --version </dev/null 2>/dev/null | head -n 1 || true)
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
	for _, p := range extraPaths {
		if !validExtra(p) {
			return "", fmt.Errorf("invalid path_extra %q", p)
		}
		fmt.Fprintf(&b, "prepend %s\n", shellquote.Quote(p))
	}
	b.WriteString(scriptFns)
	return b.String(), nil
}

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
