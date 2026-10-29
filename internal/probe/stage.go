package probe

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/indiekitai/clifleet/internal/catalog"
	"github.com/indiekitai/clifleet/internal/shellquote"
)

// StageStep is one installed tool whose artifact can be downloaded ahead
// of the binary swap. Kind is "npm", "brew", or "release".
type StageStep struct {
	Tool      string
	Kind      string
	Package   string
	BrewKind  string
	BrewToken string
	Release   string // "codex" or "claude" when Kind is release
}

// ActionResult is one fetch or apply line from the remote script.
// Skip is set when the tool had nothing staged. Exit is the command
// status otherwise. Detail is a cache path or a short reason.
type ActionResult struct {
	Tool    string
	Exit    int
	Skip    string
	Blocked string
	Current bool
	Detail  string
}

const fetchHeader = "clifleet-fetch/1"
const applyHeader = "clifleet-apply/1"

// stagePrelude is shared by fetch and apply. It only creates cache paths
// and never replaces a live binary by itself.
const stagePrelude = `
cache_root() {
  if [ -n "${XDG_CACHE_HOME:-}" ]; then
    printf '%s/clifleet\n' "$XDG_CACHE_HOME"
  elif [ -n "${HOME:-}" ]; then
    printf '%s/.cache/clifleet\n' "$HOME"
  else
    return 1
  fi
}

# cf_unstage forgets what an earlier fetch staged for a tool. Every fetch
# starts with it: the fetch replaces that tool's artifact, so a fetch that
# fails now must not leave apply an old stamp pointing at a new partial file.
cf_unstage() {
  rm -f "$1/staged/$2" "$1/staged/$2.tmp"
}

cf_report() {
  detail=$(printf '%s' "$4" | tr '\t\r\n' '   ' | cut -c1-512)
  printf 'CF1\t%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$detail"
}

cf_safe_token() {
  case $1 in
    ''|*[!A-Za-z0-9_@+.-]*|*..*) return 1 ;;
  esac
  case $1 in
    [A-Za-z0-9]*) return 0 ;;
    *) return 1 ;;
  esac
}

# cf_brew_block_reason prints a short reason when Homebrew failed because
# Xcode or the Command Line Tools are too old. Empty means some other failure.
cf_brew_block_reason() {
  file=$1
  [ -f "$file" ] || return 1
  if grep -i 'command line tools' "$file" 2>/dev/null | grep -i outdated >/dev/null 2>&1 || grep -i 'command line tools' "$file" 2>/dev/null | grep -i 'too old' >/dev/null 2>&1; then
    printf '%s\n' 'Command Line Tools are too old for Homebrew'
    return 0
  fi
  if grep -i xcode "$file" 2>/dev/null | grep -i outdated >/dev/null 2>&1 || grep -i xcode "$file" 2>/dev/null | grep -i 'too old' >/dev/null 2>&1; then
    printf '%s\n' 'Xcode is too old for Homebrew'
    return 0
  fi
  if grep -i 'xcode-select --install' "$file" >/dev/null 2>&1; then
    printf '%s\n' 'Homebrew needs Xcode Command Line Tools (xcode-select --install)'
    return 0
  fi
  return 1
}
`

const fetchFns = `
stage_npm() {
  tool=$1
  pkg=$2
  root=$(cache_root) || { cf_report fetch "$tool" 1 "no home for cache"; return 0; }
  cf_unstage "$root" "$tool"
  dest="$root/npm/$tool"
  cf_rmtree "$dest" || { cf_report fetch "$tool" 1 "could not reset cache"; return 0; }
  mkdir -p "$dest" "$root/staged" || { cf_report fetch "$tool" 1 "mkdir failed"; return 0; }
  file=$(npm pack --loglevel=error --pack-destination "$dest" "$pkg@latest" </dev/null) || {
    cf_report fetch "$tool" 1 "npm pack failed"
    return 0
  }
  file=$(printf '%s\n' "$file" | sed '/^[[:space:]]*$/d' | tail -n 1)
  case $file in
    /*|./*|../*|*/*) tarball=$file ;;
    *) tarball=$dest/$file ;;
  esac
  case $tarball in
    "$dest"/*.tgz) ;;
    *)
      cf_report fetch "$tool" 1 "npm pack did not name a tarball"
      return 0
      ;;
  esac
  case $tarball in
    *..*)
      cf_report fetch "$tool" 1 "refusing tarball path"
      return 0
      ;;
  esac
  if [ -L "$tarball" ] || [ ! -f "$tarball" ]; then
    cf_report fetch "$tool" 1 "missing tarball"
    return 0
  fi
  ver=$(cf_tarball_version "$tarball")
  eng=$(cf_tarball_engines "$tarball")
  [ -n "$ver" ] || ver=-
  [ -n "$eng" ] || eng=-
  plat=-
  if [ "$pkg" = "@openai/codex" ] && [ "$ver" != - ]; then
    plat=$(cf_pack_codex_platform "$dest" "$ver") || {
      cf_report fetch "$tool" 1 "could not pack the Codex platform package"
      return 0
    }
  fi
  tmp="$root/staged/$tool.tmp"
  printf 'npm\t%s\t%s\t%s\t%s\t%s\n' "$pkg" "$tarball" "$ver" "$eng" "$plat" > "$tmp" || { cf_report fetch "$tool" 1 "could not write stamp"; return 0; }
  mv -f "$tmp" "$root/staged/$tool" || { cf_report fetch "$tool" 1 "could not write stamp"; return 0; }
  est=$(cf_engines_status "$eng")
  case $est in
    ok) cf_report fetch "$tool" 0 "$tarball" ;;
    *) cf_report fetch "$tool" blocked "${est#blocked: }" ;;
  esac
}

stage_brew() {
  tool=$1
  bkind=$2
  token=$3
  case $bkind in
    cask|formula) ;;
    *) cf_report fetch "$tool" 1 "bad brew kind"; return 0 ;;
  esac
  root=$(cache_root) || { cf_report fetch "$tool" 1 "no home for cache"; return 0; }
  cf_unstage "$root" "$tool"
  # A failed brew update must not skip the bottle. The tap on disk can
  # still serve brew fetch, and a later tool should see the same result.
  if [ "${cf_brew_update:-}" != ok ] && [ "${cf_brew_update:-}" != fail ]; then
    if brew update >"$cf_dir/brew-update.log" 2>&1 </dev/null; then
      cf_brew_update=ok
    else
      cf_brew_update=fail
    fi
    export HOMEBREW_NO_AUTO_UPDATE=1
  fi
  rc=0
  blog=$cf_dir/brew-$tool.log
  if [ "$bkind" = cask ]; then
    brew fetch --cask "$token" >"$blog" 2>&1 </dev/null || rc=$?
  else
    brew fetch --formula "$token" >"$blog" 2>&1 </dev/null || rc=$?
  fi
  if [ "$rc" -ne 0 ]; then
    reason=$(cf_brew_block_reason "$blog" || true)
    if [ -z "$reason" ]; then
      reason=$(cf_brew_block_reason "$cf_dir/brew-update.log" || true)
    fi
    if [ -n "$reason" ]; then
      cf_report fetch "$tool" blocked "$reason"
      return 0
    fi
    if [ "${cf_brew_update:-}" = fail ]; then
      cf_report fetch "$tool" "$rc" "brew update failed; brew fetch failed"
    else
      cf_report fetch "$tool" "$rc" "brew fetch failed"
    fi
    return 0
  fi
  mkdir -p "$root/staged" || { cf_report fetch "$tool" 1 "mkdir failed"; return 0; }
  tmp="$root/staged/$tool.tmp"
  printf 'brew\t%s\t%s\n' "$bkind" "$token" > "$tmp" || { cf_report fetch "$tool" 1 "could not write stamp"; return 0; }
  mv -f "$tmp" "$root/staged/$tool" || { cf_report fetch "$tool" 1 "could not write stamp"; return 0; }
  detail="brew cache ($bkind $token)"
  if [ "${cf_brew_update:-}" = fail ]; then
    detail="$detail; brew update failed, used the existing tap"
  fi
  cf_report fetch "$tool" 0 "$detail"
}
`

const applyFns = `
apply_npm() {
  tool=$1
  root=$(cache_root) || { cf_report apply "$tool" 1 "no home for cache"; return 0; }
  stamp="$root/staged/$tool"
  if [ ! -f "$stamp" ]; then
    cf_report apply "$tool" skip "nothing staged"
    return 0
  fi
  line=$(head -n 1 "$stamp" | tr -d '\r')
  kind=$(cf_stamp_field "$line" 1)
  pkg=$(cf_stamp_field "$line" 2)
  tarball=$(cf_stamp_field "$line" 3)
  ver=$(cf_stamp_field "$line" 4)
  eng=$(cf_stamp_field "$line" 5)
  plat=$(cf_stamp_field "$line" 6)
  if [ "$kind" != npm ]; then
    cf_report apply "$tool" 1 "staged artifact is not an npm tarball"
    return 0
  fi
  dest="$root/npm/$tool"
  case $tarball in
    *..*)
      cf_report apply "$tool" 1 "refusing tarball path"
      return 0
      ;;
  esac
  case $tarball in
    "$dest"/*.tgz) ;;
    *)
      cf_report apply "$tool" 1 "staged tarball is outside the cache"
      return 0
      ;;
  esac
  if [ -L "$tarball" ]; then
    cf_report apply "$tool" 1 "refusing staged symlink"
    return 0
  fi
  if [ ! -f "$tarball" ]; then
    cf_report apply "$tool" 1 "staged tarball missing"
    return 0
  fi
  cf_apply_npm_safe "$tool" "$pkg" "$tarball" "$plat" "$ver" "$eng"
}

apply_brew() {
  tool=$1
  root=$(cache_root) || { cf_report apply "$tool" 1 "no home for cache"; return 0; }
  stamp="$root/staged/$tool"
  if [ ! -f "$stamp" ]; then
    cf_report apply "$tool" skip "nothing staged"
    return 0
  fi
  line=$(head -n 1 "$stamp" | tr -d '\r')
  tab=$(printf '\t')
  kind=${line%%$tab*}
  rest=${line#*$tab}
  bkind=${rest%%$tab*}
  token=${rest#*$tab}
  if [ "$kind" != brew ]; then
    cf_report apply "$tool" 1 "staged artifact is not a brew download"
    return 0
  fi
  case $bkind in
    cask|formula) ;;
    *) cf_report apply "$tool" 1 "bad brew stage"; return 0 ;;
  esac
  cf_safe_token "$token" || { cf_report apply "$tool" 1 "refusing unsafe brew token"; return 0; }
  rc=0
  blog=$cf_dir/brew-apply-$tool.log
  if [ "$bkind" = cask ]; then
    brew upgrade --cask "$token" >"$blog" 2>&1 </dev/null || rc=$?
  else
    brew upgrade "$token" >"$blog" 2>&1 </dev/null || rc=$?
  fi
  if [ "$rc" -ne 0 ]; then
    reason=$(cf_brew_block_reason "$blog" || true)
    if [ -n "$reason" ]; then
      cf_report apply "$tool" blocked "$reason"
      return 0
    fi
    cf_report apply "$tool" "$rc" "brew upgrade failed"
    return 0
  fi
  cf_report apply "$tool" 0 "brew cache ($bkind $token)"
}
`

// FetchScript downloads staged artifacts. It does not install or replace
// a live binary.
func FetchScript(extraPaths []string, steps []StageStep) (string, error) {
	return FetchScriptBudget(extraPaths, steps, 0)
}

// FetchScriptBudget is FetchScript with the per-tool host budget. Release
// and Homebrew downloads may use that budget instead of a fixed 180s cap.
// A zero budget keeps the built-in defaults.
func FetchScriptBudget(extraPaths []string, steps []StageStep, toolTimeout time.Duration) (string, error) {
	if len(steps) == 0 {
		return "", fmt.Errorf("no stage commands")
	}
	body, err := scriptBody(extraPaths)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(body)
	b.WriteString(stagePrelude)
	b.WriteString(fetchSafeShell)
	b.WriteString(codexPackShell)
	b.WriteString(fetchFns)
	// Allow an explicit brew update. brew fetch below turns auto-update
	// back off so a fetch cannot grow into an upgrade.
	b.WriteString(`unset HOMEBREW_NO_AUTO_UPDATE
export NONINTERACTIVE=1
export HOMEBREW_NO_ENV_HINTS=1
export HOMEBREW_NO_ANALYTICS=1
export npm_config_fund=false
export npm_config_audit=false
export npm_config_update_notifier=false
printf '%s\n' 'clifleet-fetch/1'
`)
	if fetchHasBrew(steps) {
		// One brew update for the script, in this shell. Each stage_brew then
		// runs under cf_limit, which is a subshell and would not see an update
		// performed inside another tool. A failed or capped update does not
		// skip fetch: the bottles already on disk may still install.
		b.WriteString(`if command -v brew >/dev/null 2>&1; then
  rc=0
  cf_limit 90 brew update >"$cf_dir/brew-update.log" 2>&1 || rc=$?
  if [ "$rc" -eq 0 ]; then
    cf_brew_update=ok
  else
    cf_brew_update=fail
  fi
  export HOMEBREW_NO_AUTO_UPDATE=1
fi
`)
	}
	if err := writeStageCalls(&b, steps, true, toolTimeout); err != nil {
		return "", err
	}
	b.WriteString("exit 0\n")
	return b.String(), nil
}

// ApplyScript installs from the cache written by FetchScript, then reprobes.
// The install command is the short part: a local npm tarball, or brew
// upgrade with auto-update left off.
func ApplyScript(reprobe []Tool, extraPaths []string, steps []StageStep) (string, error) {
	return ApplyScriptBudget(reprobe, extraPaths, steps, 0)
}

// ApplyScriptBudget is ApplyScript with the per-tool host budget. Apply
// stays a short swap; the budget only shrinks a cap that would outlive it.
func ApplyScriptBudget(reprobe []Tool, extraPaths []string, steps []StageStep, toolTimeout time.Duration) (string, error) {
	if len(steps) == 0 {
		return "", fmt.Errorf("no stage commands")
	}
	body, err := scriptBody(extraPaths)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(body)
	b.WriteString(stagePrelude)
	b.WriteString(applySafeShell)
	b.WriteString(applyFns)
	b.WriteString(`export NONINTERACTIVE=1
export HOMEBREW_NO_AUTO_UPDATE=1
export HOMEBREW_NO_ENV_HINTS=1
export HOMEBREW_NO_ANALYTICS=1
export npm_config_fund=false
export npm_config_audit=false
export npm_config_update_notifier=false
printf '%s\n' 'clifleet-apply/1'
`)
	if err := writeStageCalls(&b, steps, false, toolTimeout); err != nil {
		return "", err
	}
	b.WriteString("printf '%s\\n' 'clifleet-probe/1'\n")
	if err := writeProbes(&b, reprobe); err != nil {
		return "", err
	}
	b.WriteString("exit 0\n")
	return b.String(), nil
}

func writeStageCalls(b *strings.Builder, steps []StageStep, fetch bool, toolTimeout time.Duration) error {
	seen := map[string]struct{}{}
	for _, step := range steps {
		if err := validStageStep(step); err != nil {
			return err
		}
		if _, ok := seen[step.Tool]; ok {
			return fmt.Errorf("duplicate stage step for %s", step.Tool)
		}
		seen[step.Tool] = struct{}{}
		var call string
		switch step.Kind {
		case catalog.MethodNpm:
			if fetch {
				call = fmt.Sprintf("stage_npm %s %s", shellquote.Quote(step.Tool), shellquote.Quote(step.Package))
			} else {
				call = fmt.Sprintf("apply_npm %s", shellquote.Quote(step.Tool))
			}
		case catalog.MethodBrew:
			if fetch {
				call = fmt.Sprintf("stage_brew %s %s %s", shellquote.Quote(step.Tool), shellquote.Quote(step.BrewKind), shellquote.Quote(step.BrewToken))
			} else {
				call = fmt.Sprintf("apply_brew %s", shellquote.Quote(step.Tool))
			}
		case catalog.KindRelease:
			fn := "apply_release_" + step.Release
			if fetch {
				fn = "stage_release_" + step.Release
			}
			call = fmt.Sprintf("%s %s", fn, shellquote.Quote(step.Tool))
		default:
			return fmt.Errorf("unknown stage kind %q", step.Kind)
		}
		writeLimited(b, fetch, step.Kind, step.Tool, call, toolTimeout)
	}
	return nil
}

func fetchHasBrew(steps []StageStep) bool {
	for _, step := range steps {
		if step.Kind == catalog.MethodBrew {
			return true
		}
	}
	return false
}

// writeLimited caps one stage or apply so a slow download cannot hold the
// rest of the host script. npm pack stays at 120s. Homebrew and release
// fetches grow with the per-tool host budget when that budget is longer
// than the default, and they stay inside it so the script can still report
// a timeout. Apply stays a short local swap.
func writeLimited(b *strings.Builder, fetch bool, kind, tool, call string, toolTimeout time.Duration) {
	secs := limitSeconds(fetch, kind, toolTimeout)
	tag := "apply"
	if fetch {
		tag = "fetch"
	}
	fmt.Fprintf(b, "rc=0\ncf_limit %d %s || rc=$?\n", secs, call)
	fmt.Fprintf(b, "if [ \"$rc\" -eq 124 ]; then\n  cf_report %s %s 124 'timed out'\nfi\n", tag, shellquote.Quote(tool))
}

// limitSeconds is the per-tool script cap. A zero toolTimeout uses the
// defaults (npm 120s, brew fetch 180s, release fetch 480s, apply 180s).
// When the host budget is longer, release and brew fetches may use it,
// minus 20s so the script can print a CF1 line before SSH kills it. A short
// budget keeps a third of itself as that margin; the cap never reaches the
// budget, or SSH would kill the script before it reports the timeout.
func limitSeconds(fetch bool, kind string, toolTimeout time.Duration) int {
	npm, brew, release, apply := 120, 180, 480, 180
	avail := 0
	if toolTimeout > 0 {
		total := int(toolTimeout.Seconds())
		margin := 20
		if total < 60 {
			margin = total / 3
			if margin < 1 {
				margin = 1
			}
		}
		avail = total - margin
		if avail < 1 {
			avail = 1
		}
	}
	if !fetch {
		secs := apply
		if kind == catalog.MethodBrew {
			secs = brew
		}
		if avail > 0 && secs > avail {
			secs = avail
		}
		return secs
	}
	secs := npm
	switch kind {
	case catalog.MethodBrew:
		secs = brew
		if avail > secs {
			secs = avail
		}
	case catalog.KindRelease:
		secs = release
		if avail > secs {
			secs = avail
		}
	}
	if avail > 0 && secs > avail {
		return avail
	}
	return secs
}

func validStageStep(step StageStep) error {
	if !nameRe.MatchString(step.Tool) {
		return fmt.Errorf("invalid tool name %q", step.Tool)
	}
	switch step.Kind {
	case catalog.MethodNpm:
		if !catalog.PackageOK(step.Package) {
			return fmt.Errorf("refusing unsafe package %q", step.Package)
		}
	case catalog.MethodBrew:
		if !catalog.BrewTokenOK(step.BrewToken) || !catalog.BrewKindOK(step.BrewKind) {
			return fmt.Errorf("refusing unsafe brew target %q", step.BrewToken)
		}
	case catalog.KindRelease:
		if step.Release != "codex" && step.Release != "claude" && step.Release != "agent" && step.Release != "cursor_agent" {
			return fmt.Errorf("unknown release %q", step.Release)
		}
	default:
		return fmt.Errorf("unknown stage kind %q", step.Kind)
	}
	return nil
}

// ParseFetch reads a fetch script's stdout.
func ParseFetch(stdout string) ([]ActionResult, error) {
	return parseActionLines(stdout, fetchHeader, "fetch")
}

// ParseApply reads an apply script's stdout: command statuses plus a fresh probe.
func ParseApply(stdout string) ([]ActionResult, map[string]catalog.Observation, error) {
	if !strings.Contains(stdout, applyHeader) {
		return nil, nil, fmt.Errorf("remote apply did not return a clifleet result")
	}
	results, err := parseActionLines(stdout, applyHeader, "apply")
	if err != nil {
		return nil, nil, err
	}
	obs, err := ParseProbe(stdout)
	if err != nil {
		return results, nil, err
	}
	return results, obs, nil
}

func parseActionLines(stdout, header, tag string) ([]ActionResult, error) {
	if !strings.Contains(stdout, header) {
		return nil, fmt.Errorf("remote %s did not return a clifleet result", tag)
	}
	var out []ActionResult
	seen := map[string]struct{}{}
	prefix := "CF1\t" + tag + "\t"
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimRight(line, "\r")
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 4 {
			return nil, fmt.Errorf("short %s line %q", tag, line)
		}
		res := ActionResult{Tool: fields[2]}
		if len(fields) >= 5 {
			res.Detail = strings.TrimSpace(fields[4])
		}
		if _, ok := seen[res.Tool]; ok {
			return nil, fmt.Errorf("duplicate %s result for %s", tag, res.Tool)
		}
		seen[res.Tool] = struct{}{}
		switch fields[3] {
		case "skip":
			res.Skip = res.Detail
			if res.Skip == "" {
				res.Skip = "skipped"
			}
		case "blocked":
			res.Blocked = res.Detail
			if res.Blocked == "" {
				res.Blocked = "blocked"
			}
		case "current":
			res.Current = true
			if res.Detail == "" {
				res.Detail = "up to date"
			}
		default:
			code, err := strconv.Atoi(strings.TrimSpace(fields[3]))
			if err != nil {
				return nil, fmt.Errorf("bad %s status %q", tag, fields[3])
			}
			res.Exit = code
		}
		out = append(out, res)
	}
	return out, nil
}
