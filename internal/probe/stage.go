package probe

import (
	"fmt"
	"strconv"
	"strings"

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
`

const fetchFns = `
stage_npm() {
  tool=$1
  pkg=$2
  root=$(cache_root) || { cf_report fetch "$tool" 1 "no home for cache"; return 0; }
  dest="$root/npm/$tool"
  rm -rf "$dest" || { cf_report fetch "$tool" 1 "could not reset cache"; return 0; }
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
  if [ "${cf_brew_update:-}" != ok ]; then
    if [ "${cf_brew_update:-}" = fail ]; then
      cf_report fetch "$tool" 1 "brew update failed"
      return 0
    fi
    if brew update </dev/null >&2; then
      cf_brew_update=ok
      # The tap is current. Later brew commands in this fetch must not
      # update again, and must not upgrade an installed formula.
      export HOMEBREW_NO_AUTO_UPDATE=1
    else
      cf_brew_update=fail
      cf_report fetch "$tool" 1 "brew update failed"
      return 0
    fi
  fi
  rc=0
  if [ "$bkind" = cask ]; then
    brew fetch --cask "$token" </dev/null >&2 || rc=$?
  else
    brew fetch --formula "$token" </dev/null >&2 || rc=$?
  fi
  if [ "$rc" -ne 0 ]; then
    cf_report fetch "$tool" "$rc" "brew fetch failed"
    return 0
  fi
  root=$(cache_root) || { cf_report fetch "$tool" 1 "no home for cache"; return 0; }
  mkdir -p "$root/staged" || { cf_report fetch "$tool" 1 "mkdir failed"; return 0; }
  tmp="$root/staged/$tool.tmp"
  printf 'brew\t%s\t%s\n' "$bkind" "$token" > "$tmp" || { cf_report fetch "$tool" 1 "could not write stamp"; return 0; }
  mv -f "$tmp" "$root/staged/$tool" || { cf_report fetch "$tool" 1 "could not write stamp"; return 0; }
  cf_report fetch "$tool" 0 "brew cache ($bkind $token)"
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
  cf_apply_npm_safe "$tool" "$pkg" "$tarball" "$plat"
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
  if [ "$bkind" = cask ]; then
    brew upgrade --cask "$token" </dev/null >&2 || rc=$?
  else
    brew upgrade "$token" </dev/null >&2 || rc=$?
  fi
  if [ "$rc" -ne 0 ]; then
    cf_report apply "$tool" "$rc" "brew upgrade failed"
    return 0
  fi
  cf_report apply "$tool" 0 "brew cache ($bkind $token)"
}
`

// FetchScript downloads staged artifacts. It does not install or replace
// a live binary.
func FetchScript(extraPaths []string, steps []StageStep) (string, error) {
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
	if err := writeStageCalls(&b, steps, true); err != nil {
		return "", err
	}
	b.WriteString("exit 0\n")
	return b.String(), nil
}

// ApplyScript installs from the cache written by FetchScript, then reprobes.
// The install command is the short part: a local npm tarball, or brew
// upgrade with auto-update left off.
func ApplyScript(reprobe []Tool, extraPaths []string, steps []StageStep) (string, error) {
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
	if err := writeStageCalls(&b, steps, false); err != nil {
		return "", err
	}
	b.WriteString("printf '%s\\n' 'clifleet-probe/1'\n")
	if err := writeProbes(&b, reprobe); err != nil {
		return "", err
	}
	b.WriteString("exit 0\n")
	return b.String(), nil
}

func writeStageCalls(b *strings.Builder, steps []StageStep, fetch bool) error {
	seen := map[string]struct{}{}
	for _, step := range steps {
		if err := validStageStep(step); err != nil {
			return err
		}
		if _, ok := seen[step.Tool]; ok {
			return fmt.Errorf("duplicate stage step for %s", step.Tool)
		}
		seen[step.Tool] = struct{}{}
		switch step.Kind {
		case catalog.MethodNpm:
			fn := "apply_npm"
			if fetch {
				fn = "stage_npm"
				fmt.Fprintf(b, "%s %s %s\n", fn, shellquote.Quote(step.Tool), shellquote.Quote(step.Package))
				continue
			}
			fmt.Fprintf(b, "%s %s\n", fn, shellquote.Quote(step.Tool))
		case catalog.MethodBrew:
			if fetch {
				fmt.Fprintf(b, "stage_brew %s %s %s\n", shellquote.Quote(step.Tool), shellquote.Quote(step.BrewKind), shellquote.Quote(step.BrewToken))
				continue
			}
			fmt.Fprintf(b, "apply_brew %s\n", shellquote.Quote(step.Tool))
		case catalog.KindRelease:
			fn := "apply_release_" + step.Release
			if fetch {
				fn = "stage_release_" + step.Release
			}
			fmt.Fprintf(b, "%s %s\n", fn, shellquote.Quote(step.Tool))
		default:
			return fmt.Errorf("unknown stage kind %q", step.Kind)
		}
	}
	return nil
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
