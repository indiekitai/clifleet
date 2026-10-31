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
# Terminal apps put per-session wrapper scripts named after the CLIs ahead of
# them (cmux: ~/.cmuxterm/cmux-cli-shims/<id>/amp). They are not installs.
cf_p_new=
IFS=:
for cf_p_dir in $PATH; do
  case $cf_p_dir in
    */cmux-cli-shims|*/cmux-cli-shims/*) continue ;;
  esac
  cf_p_new=${cf_p_new:+$cf_p_new:}$cf_p_dir
done
unset IFS
PATH=${cf_p_new:-/usr/bin:/bin}
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
	`/home/linuxbrew/.linuxbrew/sbin`,
	`/home/linuxbrew/.linuxbrew/bin`,
	`/usr/local/sbin`,
	`/usr/local/bin`,
	`/opt/homebrew/sbin`,
	`/opt/homebrew/bin`,
	// User linuxbrew is last so it is searched before a system brew. A test
	// mock at ~/.linuxbrew/bin must win over /home/linuxbrew or /usr/local.
	`"$HOME/.linuxbrew/sbin"`,
	`"$HOME/.linuxbrew/bin"`,
}

// brewFallbacks are absolute brew paths tried when brew is not on PATH.
// Tests replace this and loginPathDirs so a script run on a developer
// machine cannot reach the real Homebrew prefix.
var brewFallbacks = []string{
	`/opt/homebrew/bin/brew`,
	`/usr/local/bin/brew`,
	`/home/linuxbrew/.linuxbrew/bin/brew`,
}

// shimPathDirs are version-manager shims. They are appended after the real
// prefixes so a mise or asdf shim cannot shadow ~/.local/bin. A shim that
// is still the command we find is left alone; it is not the install target.
var shimPathDirs = []string{
	`"$HOME/.local/share/mise/shims"`,
	`"$HOME/.asdf/shims"`,
}

const scriptFns = `
# cf_rmtree PATH is the only rm -rf in these scripts. Helpers share global
# variables, so a path one of them clobbered or left empty must not become
# rm -rf of /, $HOME, or a parent of $HOME. It also refuses relative paths
# and . or .. components, and says so on stderr.
cf_rmtree() {
  cf_rt_p=$1
  while :; do
    case $cf_rt_p in
      ?*/) cf_rt_p=${cf_rt_p%/} ;;
      *) break ;;
    esac
  done
  case $cf_rt_p in
    '' | / | [!/]* | */. | */./* | */.. | */../*)
      printf 'clifleet: refused to remove %s\n' "$1" >&2
      return 1
      ;;
  esac
  case ${HOME:-/}/ in
    "$cf_rt_p"/*)
      printf 'clifleet: refused to remove %s\n' "$1" >&2
      return 1
      ;;
  esac
  rm -rf "$cf_rt_p"
}

# cf_kill_tree PID stops PID and everything it started. Killing only PID
# leaves the downloads and package managers a capped function started
# running as orphans. The tree is read before the first signal, because once
# PID dies its children are re-parented and no longer traceable to it.
# Processes are named by PID and start time, so the KILL a second later skips
# a PID the system handed to someone else meanwhile, yet still reaches a
# re-parented child that ignored TERM. Only the watchdog subshells call it,
# so its variables never reach a caller.
cf_kill_tree() {
  cf_kt_ids=$(cf_tree "$1:*")
  kill "$1" $(cf_ids_pids $cf_kt_ids) 2>/dev/null || true
  sleep 1
  case $cf_kt_ids in
    *:_*)
      cf_kt_ids=$(cf_tree $cf_kt_ids)
      if [ -n "$cf_kt_ids" ]; then
        kill -9 $(cf_ids_pids $cf_kt_ids) 2>/dev/null || true
      fi
      ;;
    *)
      # No start times (BusyBox ps) or no ps at all: only the direct child
      # gets the KILL, and only while it is still a sibling of this watchdog.
      # Once the caller reaps it, its PID can go to anyone; the caller forks
      # nothing while it waits for the watchdog, so a sibling is still ours.
      cf_kt_me=$(exec sh -c 'echo $PPID')
      cf_kt_same=$({ ps -A -o pid= -o ppid= 2>/dev/null || ps -o pid= -o ppid= 2>/dev/null; } | awk -v me="$cf_kt_me" -v kid="$1" '
        $1 == me { a = $2 }
        $1 == kid { b = $2 }
        END { if (a != "" && a == b) print "yes" }')
      if [ "$cf_kt_same" = yes ]; then
        kill -9 "$1" 2>/dev/null || true
      fi
      ;;
  esac
}

# cf_tree ID... prints PID:START for each live process matching an ID, and
# for all of their descendants. An ID is PID:START, or PID:* for any start.
# Without a start time (BusyBox ps has no lstart) only PID:* matches.
cf_tree() {
  { ps -A -o pid= -o ppid= -o lstart= 2>/dev/null || ps -A -o pid= -o ppid= 2>/dev/null || ps -o pid= -o ppid= 2>/dev/null; } | awk -v ids="$*" '
    BEGIN { n = split(ids, a, " "); for (i = 1; i <= n; i++) want[a[i]] = 1 }
    {
      st = ""
      for (i = 3; i <= NF; i++) st = st "_" $i
      parent[$1] = $2
      start[$1] = st
    }
    END {
      for (p in parent) if ((start[p] != "" && (p ":" start[p]) in want) || ((p ":*") in want)) seen[p] = 1
      changed = 1
      while (changed) {
        changed = 0
        for (p in parent) {
          if (!(p in seen) && (parent[p] in seen)) { seen[p] = 1; changed = 1 }
        }
      }
      for (p in seen) print p ":" start[p]
    }'
}

cf_ids_pids() {
  for cf_ip in "$@"; do
    printf '%s\n' "${cf_ip%%:*}"
  done
}

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
      cf_kill_tree "$pid"
    fi
  ) </dev/null >/dev/null 2>&1 &
  kpid=$!
  status=0
  wait "$pid" 2>/dev/null || status=$?
  if [ -f "$flag" ]; then
    # The cap fired: let the watchdog finish, so a child that ignored TERM
    # still gets its KILL.
    wait "$kpid" 2>/dev/null || true
    return 124
  fi
  kill "$kpid" 2>/dev/null || true
  wait "$kpid" 2>/dev/null || true
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
      cf_kill_tree "$pid"
    fi
  ) </dev/null >/dev/null 2>&1 &
  kpid=$!
  status=0
  wait "$pid" 2>/dev/null || status=$?
  if [ -f "$flag" ]; then
    # The cap fired: let the watchdog finish, so a child that ignored TERM
    # still gets its KILL.
    wait "$kpid" 2>/dev/null || true
    return 124
  fi
  kill "$kpid" 2>/dev/null || true
  wait "$kpid" 2>/dev/null || true
  return "$status"
}

# cf_cap_both is cf_cap with stderr kept in a separate file. Apply uses it
# to tell a locked macOS login keychain from a binary that printed no version.
cf_cap_both() {
  secs=$1
  outfile=$2
  errfile=$3
  shift 3
  : >"$outfile"
  : >"$errfile"
  flag="$outfile.t"
  rm -f "$flag"
  "$@" </dev/null >"$outfile" 2>"$errfile" &
  pid=$!
  (
    sleep "$secs"
    if kill -0 "$pid" 2>/dev/null; then
      printf '1\n' >"$flag"
      cf_kill_tree "$pid"
    fi
  ) </dev/null >/dev/null 2>&1 &
  kpid=$!
  status=0
  wait "$pid" 2>/dev/null || status=$?
  if [ -f "$flag" ]; then
    # The cap fired: let the watchdog finish, so a child that ignored TERM
    # still gets its KILL.
    wait "$kpid" 2>/dev/null || true
    return 124
  fi
  kill "$kpid" 2>/dev/null || true
  wait "$kpid" 2>/dev/null || true
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
  case $target in
    */../*|*/./*)
      # A relative link leaves ../ behind. Resolve the directory physically so
      # install-layout checks see where the file really is.
      cf_rs_dir=$(cd -P "$(dirname "$target")" 2>/dev/null && pwd -P) && target=$cf_rs_dir/$(basename "$target")
      ;;
  esac
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

cf_tab=$(printf '\t')
cf_cr=$(printf '\r')
cf_nl='
'

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
  verfile=$cf_dir/ver
  errfile=$cf_dir/ver.err
  ver=
  # 15s: a cold macOS CLI (opencode after brew) can miss a 3s cap and
  # report "version timed out" after the install already succeeded.
  if cf_cap_both 15 "$verfile" "$errfile" "$path" --version; then
    ver=$(head -n 1 "$verfile" 2>/dev/null || true)
  else
    rc=$?
    ver=$(head -n 1 "$verfile" 2>/dev/null || true)
    if [ -z "$ver" ] && [ "$rc" -eq 124 ]; then
      ver="version timed out"
    fi
  fi
  if [ -z "$ver" ] || [ "$ver" = "version timed out" ]; then
    case $token in
      ''|-*|*[!A-Za-z0-9_@+.-]*) ;;
      *)
        if command -v brew >/dev/null 2>&1; then
          bfile=$cf_dir/brewver
          if cf_cap 8 "$bfile" brew list --versions "$token"; then
            bver=$(head -n 1 "$bfile" 2>/dev/null | awk '{print $2}')
            if [ -n "$bver" ]; then
              ver=$bver
            fi
          fi
        fi
        ;;
    esac
  fi
  if [ -z "$ver" ] || [ "$ver" = "version timed out" ]; then
    noise=$(cat "$verfile" "$errfile" 2>/dev/null || true)
    low=$(printf '%s\n' "$noise" | tr 'A-Z' 'a-z')
    case $low in
      *'keychain is locked'*|*'login keychain'*) ver="keychain locked" ;;
    esac
  fi
  case "$path$real" in
    *"$cf_tab"*|*"$cf_nl"*|*"$cf_cr"*)
      # The fields are tab-separated and line-based; a shifted field would
      # point a later apply at the wrong file. No result fails this tool only.
      printf 'clifleet: %s resolves to a path with a tab or newline; skipped\n' "$label" >&2
      return 0
      ;;
  esac
  ver=$(flatten "$ver")
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
	b.WriteString(stagedFn)
	for _, t := range tools {
		fmt.Fprintf(&b, "probe_staged %s\n", shellquote.Quote(t.Name))
	}
	b.WriteString("exit 0\n")
	return b.String(), nil
}

// stagedFn reads the stamp a fetch left for a tool, so an --apply dry run
// can say what it would apply. It only reads. The version sits in a
// different field per kind: npm 4, Claude / Cursor Agent 3, Codex 5; brew
// and older Codex stamps have none. A blocked release stamp carries its
// reason instead.
const stagedFn = `
probe_staged() {
  if [ -n "${XDG_CACHE_HOME:-}" ]; then
    cf_ps_f="$XDG_CACHE_HOME/clifleet/staged/$1"
  elif [ -n "${HOME:-}" ]; then
    cf_ps_f="$HOME/.cache/clifleet/staged/$1"
  else
    return 0
  fi
  [ -f "$cf_ps_f" ] && [ ! -L "$cf_ps_f" ] || return 0
  cf_ps_line=$(head -n 1 "$cf_ps_f" 2>/dev/null | tr -d '\r')
  cf_ps_ver=
  cf_ps_why=
  case $(printf '%s\n' "$cf_ps_line" | awk -F '\t' '{print $1 "/" $2 "/" $3}') in
    npm/*) cf_ps_n=4 ;;
    release/*/blocked)
      cf_ps_n=
      cf_ps_why=$(printf '%s\n' "$cf_ps_line" | awk -F '\t' '{print $4}' | tr -d '\t' | cut -c1-200)
      [ -n "$cf_ps_why" ] || cf_ps_why=blocked
      ;;
    release/claude/*|release/cursor-agent/*) cf_ps_n=3 ;;
    release/codex/*) cf_ps_n=5 ;;
    *) cf_ps_n= ;;
  esac
  if [ -n "$cf_ps_n" ]; then
    cf_ps_ver=$(printf '%s\n' "$cf_ps_line" | awk -F '\t' -v n="$cf_ps_n" '{print $n}')
  fi
  case $cf_ps_ver in
    ''|-|*[!A-Za-z0-9._+-]*) cf_ps_ver= ;;
  esac
  printf 'CF1\tstaged\t%s\t%s\t%s\n' "$1" "$cf_ps_ver" "$cf_ps_why"
}
`

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
	writeExtraFirstFn(&b, extraPaths)
	b.WriteString(scriptFns)
	b.WriteString(strings.Replace(brewEnvSnippet, "BREW_FALLBACKS", strings.Join(brewFallbacks, " "), 1))
	b.WriteString(nvmSnippet)
	return b.String(), nil
}

func writePathFn(b *strings.Builder, name, helper string, extra []string) error {
	fmt.Fprintf(b, "%s() {\n", name)
	for _, dir := range loginPathDirs {
		fmt.Fprintf(b, "  %s %s\n", helper, dir)
	}
	if helper == "append_missing" {
		for _, dir := range shimPathDirs {
			fmt.Fprintf(b, "  %s %s\n", helper, dir)
		}
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

// writeExtraFirstFn writes cf_extra_first, which puts path_extra back at the
// front of PATH. brew shellenv and nvm prepend their own directories, so without
// this /opt/homebrew/bin would shadow a path_extra binary the host asked for.
// Callers validated the entries in writePathFn.
func writeExtraFirstFn(b *strings.Builder, extra []string) {
	b.WriteString("cf_extra_first() {\n  :\n")
	for _, p := range extra {
		fmt.Fprintf(b, "  prepend %s\n", shellquote.Quote(p))
	}
	b.WriteString("}\n")
}

// brewEnvSnippet is the non-interactive equivalent of a login shell's
// eval "$(brew shellenv)". Absolute paths are checked first because ssh -T
// starts with PATH=/usr/bin:/bin:/usr/sbin:/sbin. Output is capped so a stuck
// brew cannot hold the probe. This is not bash -lc.
const brewEnvSnippet = `
cf_brew_env() {
  # PATH was just prepended, so command -v sees ~/.linuxbrew, path_extra, and
  # /opt/homebrew before a brew that happens to exist elsewhere on the machine.
  # Absolute paths are only a fallback for ssh -T if the directory test missed.
  brewbin=
  if command -v brew >/dev/null 2>&1; then
    brewbin=$(command -v brew)
  fi
  for b in BREW_FALLBACKS; do
    if [ -z "$brewbin" ] && [ -x "$b" ]; then
      brewbin=$b
    fi
  done
  if [ -z "$brewbin" ]; then
    return 0
  fi
  envfile=$cf_dir/brew.env
  if cf_cap 5 "$envfile" "$brewbin" shellenv; then
    # shellenv is a few export lines. Apply them without bash -lc.
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
trap 'cf_rmtree "$cf_dir"' EXIT
trap 'cf_rmtree "$cf_dir"; exit 143' INT TERM
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
cf_brew_env
# shellenv and nvm both replace PATH. Put the standard directories back
# afterwards so a login-style brew prefix cannot drop ~/.linuxbrew or
# /opt/homebrew, and a wiped nvm PATH cannot drop brew.
cf_ensure_path
cf_extra_first
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
			if len(fields) != 9 {
				return nil, fmt.Errorf("present line has %d fields, want 9: %q", len(fields), line)
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
		case "staged":
			o, ok := obs[fields[2]]
			if !ok || !o.Present {
				continue
			}
			o.Staged = true
			if len(fields) > 3 {
				o.StagedVersion = strings.TrimSpace(fields[3])
			}
			if len(fields) > 4 {
				o.StagedBlocked = strings.TrimSpace(fields[4])
			}
			obs[fields[2]] = o
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
