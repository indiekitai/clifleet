// Package catalog decides which upgrade command to run for a tool that is
// already installed. It never returns a command for a tool that is absent.
package catalog

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/indiekitai/clifleet/internal/shellquote"
)

const (
	MethodBrew   = "brew"
	MethodNpm    = "npm"
	MethodBun    = "bun"
	MethodPnpm   = "pnpm"
	MethodYarn   = "yarn"
	MethodCargo  = "cargo"
	MethodNative = "native"
)

// Spec is how to upgrade one tool when the install method is known.
type Spec struct {
	Name     string
	Binary   string
	Native   []string
	Npm      string
	Brew     string
	BrewKind string // "cask" or "formula"
}

// Overlay is a user recipe. Empty fields do not erase a built-in spec.
type Overlay struct {
	Name     string
	Binary   string
	Native   []string
	Npm      string
	Brew     string
	BrewKind string
}

// Observation is what a host reported about one binary.
type Observation struct {
	Tool      string
	Present   bool
	Path      string
	Real      string
	Version   string
	BrewKind  string
	BrewToken string
	Package   string
}

// Command is a non-interactive upgrade argv. It is only meaningful when
// the tool is already installed.
//
// Package, BrewToken, and BrewKind are set for package-manager commands so
// a staged fetch can reuse the same resolved target. They are empty for a
// native updater.
type Command struct {
	Argv      []string
	Method    string
	Display   string
	Package   string
	BrewToken string
	BrewKind  string
}

// Decision is the per-tool plan. Command is nil when nothing should run.
type Decision struct {
	Method  string
	Command *Command
	Skip    string // human reason; empty when a command will run
	Fail    string // hard failure; nothing is run
}

// DefaultTools are the candidates clifleet looks for unless the fleet file
// says otherwise. This is a curated worldwide set, not every agent name a
// model router knows. Regional CLIs and tools whose binary name moves around
// stay out; some of those still have a built-in spec so tools=["dsh"] upgrades
// an install that is already there without a [[recipe]].
func DefaultTools() []string {
	return []string{
		"claude", "codex", "opencode",
		"cursor", "agent", "cursor-agent",
		"gemini", "agy",
		"copilot", "amp", "pi", "aider", "goose",
		"openclaw", "hermes", "kimi", "qwen",
	}
}

// Builtin specs match upstream install channels as of October 2026.
// A native argv is set only when that command is the documented updater.
// Package-manager installs are upgraded with that package manager, because
// the native updater is the wrong command for a Homebrew cask or an npm global.
// Fields that are not set are intentional: an unclear channel is a skip
// ("no upgrade recipe"), not a guessed installer.
func builtin(name string) (Spec, bool) {
	switch name {
	case "claude":
		return Spec{
			Name:     "claude",
			Binary:   "claude",
			Native:   []string{"claude", "update"},
			Npm:      "@anthropic-ai/claude-code",
			Brew:     "claude-code",
			BrewKind: "cask",
		}, true
	case "codex":
		return Spec{
			Name:     "codex",
			Binary:   "codex",
			Native:   []string{"codex", "update"},
			Npm:      "@openai/codex",
			Brew:     "codex",
			BrewKind: "cask",
		}, true
	case "opencode":
		return Spec{
			Name:     "opencode",
			Binary:   "opencode",
			Native:   []string{"opencode", "upgrade"},
			Brew:     "opencode",
			BrewKind: "formula",
			// npm package is not guessed: v1 is opencode-ai, v2 is @opencode/cli.
			// The package name has to come from the install on disk.
		}, true
	case "cursor":
		// Editor shell command, distinct from the Cursor Agent CLI (`agent`
		// and `cursor-agent`). The app shim has no updater of its own; the
		// app updates itself. Homebrew's cask is `cursor`.
		return Spec{
			Name:     "cursor",
			Binary:   "cursor",
			Brew:     "cursor",
			BrewKind: "cask",
		}, true
	case "agent":
		// Cursor Agent CLI. The install script links this name. Official
		// manual update is `agent update`. Homebrew's cask installs
		// `cursor-agent` instead, so this spec has no brew token.
		return Spec{
			Name:   "agent",
			Binary: "agent",
			Native: []string{"agent", "update"},
		}, true
	case "cursor-agent":
		// Same Cursor Agent CLI under the name Homebrew links (cask
		// `cursor-cli`) and the install script also creates. Not an alias
		// of the `cursor` editor command.
		return Spec{
			Name:     "cursor-agent",
			Binary:   "cursor-agent",
			Native:   []string{"cursor-agent", "update"},
			Brew:     "cursor-cli",
			BrewKind: "cask",
		}, true
	case "gemini":
		// Google Gemini CLI. FAQ: npm i -g @google/gemini-cli, brew formula
		// gemini-cli. There is no verified self-update subcommand (the
		// cheatsheet mentions `gemini update`; the command reference and the
		// update PRs do not). A native-looking binary is skipped.
		return Spec{
			Name:     "gemini",
			Binary:   "gemini",
			Npm:      "@google/gemini-cli",
			Brew:     "gemini-cli",
			BrewKind: "formula",
		}, true
	case "agy":
		// Google Antigravity CLI. Native installer, `agy update`. No npm
		// package and no Homebrew formula.
		return Spec{
			Name:   "agy",
			Binary: "agy",
			Native: []string{"agy", "update"},
		}, true
	case "copilot":
		return Spec{
			Name:     "copilot",
			Binary:   "copilot",
			Native:   []string{"copilot", "update"},
			Npm:      "@github/copilot",
			Brew:     "copilot-cli",
			BrewKind: "cask",
		}, true
	case "amp":
		// Current npm name is @ampcode/cli. @sourcegraph/amp is the old name
		// and is recognized from the path, not used as the fallback.
		return Spec{
			Name:   "amp",
			Binary: "amp",
			Native: []string{"amp", "update"},
			Npm:    "@ampcode/cli",
		}, true
	case "pi":
		// `pi update` updates the CLI only. `pi update pi` is the same
		// self-update; `pi update --all` also updates extensions, so it is
		// not used here.
		return Spec{
			Name:   "pi",
			Binary: "pi",
			Native: []string{"pi", "update"},
			Npm:    "@earendil-works/pi-coding-agent",
		}, true
	case "aider":
		// PyPI/uv installs take `aider --upgrade`. --yes-always answers the
		// install prompt so stdin /dev/null does not stall it. Homebrew
		// formula is `aider` (the Python package name is aider-chat).
		return Spec{
			Name:     "aider",
			Binary:   "aider",
			Native:   []string{"aider", "--upgrade", "--yes-always"},
			Brew:     "aider",
			BrewKind: "formula",
		}, true
	case "goose":
		// Block's goose CLI. `goose update` is the script install's updater.
		// The Homebrew formula is block-goose-cli. The formula named goose
		// is a different program that also ships a `goose` binary; when that
		// one is on disk, the Cellar token is used instead of this fallback.
		return Spec{
			Name:     "goose",
			Binary:   "goose",
			Native:   []string{"goose", "update"},
			Brew:     "block-goose-cli",
			BrewKind: "formula",
		}, true
	case "openclaw":
		// `openclaw update --yes` is the non-interactive updater and picks
		// the package manager itself (npm needs --allow-scripts=openclaw).
		// No Npm/Brew fallback, so a brew-classified copy with no token on
		// disk is skipped rather than pointed at a guessed formula.
		return Spec{
			Name:   "openclaw",
			Binary: "openclaw",
			Native: []string{"openclaw", "update", "--yes"},
		}, true
	case "hermes":
		// Nous Research Hermes. `hermes update` moves a managed source
		// checkout to the next stable release. It does not rewrite
		// package-manager-owned installs (Store, Nix, Docker, Termux APT).
		return Spec{
			Name:   "hermes",
			Binary: "hermes",
			Native: []string{"hermes", "update"},
		}, true
	case "kimi":
		// Kimi Code. `kimi upgrade --yes` skips the confirmation prompt.
		// The separate PyPI project kimi-cli is not this binary.
		return Spec{
			Name:   "kimi",
			Binary: "kimi",
			Native: []string{"kimi", "upgrade", "--yes"},
			Npm:    "@moonshot-ai/kimi-code",
		}, true
	case "qwen":
		return Spec{
			Name:   "qwen",
			Binary: "qwen",
			Native: []string{"qwen", "update"},
			Npm:    "@qwen-code/qwen-code",
		}, true

	// Opt-in. Present in builtin so tools=["name"] upgrades an install
	// that is already there. Not in DefaultTools.
	case "dsh":
		// DeepSeek Harness. Global npm package @deepseek-ai/dsh. The CLI
		// has no `dsh update` subcommand; a non-npm binary is skipped.
		return Spec{
			Name:   "dsh",
			Binary: "dsh",
			Npm:    "@deepseek-ai/dsh",
		}, true
	case "grok":
		// xAI Grok Build. Binary is `grok`, not grok-build.
		return Spec{
			Name:   "grok",
			Binary: "grok",
			Native: []string{"grok", "update"},
			Npm:    "@xai-official/grok",
		}, true
	case "kilo":
		// Kilo Code. Docs use the `kilo` binary and `kilo upgrade`.
		return Spec{
			Name:   "kilo",
			Binary: "kilo",
			Native: []string{"kilo", "upgrade"},
			Npm:    "@kilocode/cli",
		}, true
	case "kiro-cli":
		// AWS Kiro CLI. Self-update is non-interactive with this flag.
		// No npm package and no Homebrew formula.
		return Spec{
			Name:   "kiro-cli",
			Binary: "kiro-cli",
			Native: []string{"kiro-cli", "update", "--non-interactive"},
		}, true
	default:
		return Spec{}, false
	}
}

// Resolve returns the spec for name, with recipe overlays applied.
func Resolve(name string, overlays []Overlay) Spec {
	name = strings.ToLower(name)
	spec, ok := builtin(name)
	if !ok {
		spec = Spec{Name: name, Binary: name}
	}
	for _, o := range overlays {
		if !strings.EqualFold(o.Name, name) {
			continue
		}
		if o.Binary != "" {
			spec.Binary = o.Binary
		}
		if len(o.Native) > 0 {
			spec.Native = append([]string(nil), o.Native...)
		}
		if o.Npm != "" {
			spec.Npm = o.Npm
		}
		if o.Brew != "" {
			spec.Brew = o.Brew
		}
		if o.BrewKind != "" {
			spec.BrewKind = o.BrewKind
		}
	}
	if spec.Binary == "" {
		spec.Binary = spec.Name
	}
	if spec.Name == "" {
		spec.Name = name
	}
	return spec
}

// HostPolicy is the per-host allowlist and blocklist.
type HostPolicy struct {
	Tools []string
	Skip  []string
}

// Candidates is the list of tools to look for on one host.
//
// An allowlist replaces the global candidate list. A blocklist always
// removes tools. --only narrows further and cannot add a tool that policy
// excluded, so a one-off flag cannot pull a regional CLI onto a host that
// does not list it.
func Candidates(global []string, host HostPolicy, only []string) []string {
	base := global
	if len(host.Tools) > 0 {
		base = host.Tools
	}
	if len(base) == 0 {
		base = DefaultTools()
	}
	skip := setOf(host.Skip)
	onlySet := setOf(only)
	var out []string
	seen := map[string]struct{}{}
	for _, name := range base {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		if _, ok := skip[name]; ok {
			continue
		}
		if len(onlySet) > 0 {
			if _, ok := onlySet[name]; !ok {
				continue
			}
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

// Decide chooses an upgrade command for an observation.
// A tool that is not installed yields a skip and a nil command.
func Decide(spec Spec, obs Observation, methodOverride string) Decision {
	if !obs.Present {
		return Decision{Skip: "not installed"}
	}
	method := Classify(obs)
	if methodOverride != "" {
		method = methodOverride
	}
	if method == MethodCargo && methodOverride != MethodNative {
		return Decision{Method: MethodCargo, Skip: "cargo installs are not upgraded automatically"}
	}

	cmd, err := commandFor(spec, obs, method)
	if err != nil {
		// A missing recipe is a skip. An unsafe token from the remote is a
		// failure: we will not splice it into a shell script.
		if strings.HasPrefix(err.Error(), "refusing ") || strings.HasPrefix(err.Error(), "unknown ") {
			return Decision{Method: method, Fail: err.Error()}
		}
		return Decision{Method: method, Skip: err.Error()}
	}
	return Decision{Method: cmd.Method, Command: cmd}
}

// Classify guesses the install channel from the resolved binary path.
func Classify(obs Observation) string {
	if obs.BrewKind == "cask" || obs.BrewKind == "formula" || obs.BrewToken != "" {
		return MethodBrew
	}
	p := obs.Real
	if p == "" {
		p = obs.Path
	}
	switch {
	case strings.Contains(p, "/Caskroom/") || strings.Contains(p, "/Cellar/"):
		return MethodBrew
	case strings.Contains(p, "/.bun/") || strings.Contains(p, "/bun/install/"):
		return MethodBun
	case strings.Contains(p, "/pnpm/") || strings.Contains(p, "/.pnpm/"):
		return MethodPnpm
	case strings.Contains(p, "/.yarn/") || strings.Contains(p, "/yarn/"):
		return MethodYarn
	case strings.Contains(p, "node_modules") || strings.Contains(p, "/.volta/tools/"):
		return MethodNpm
	case strings.Contains(p, "/homebrew/") || strings.Contains(p, "/linuxbrew/"):
		return MethodBrew
	case strings.Contains(p, "/.cargo/"):
		return MethodCargo
	default:
		return MethodNative
	}
}

func commandFor(spec Spec, obs Observation, method string) (*Command, error) {
	switch method {
	case MethodBrew:
		return brewCommand(spec, obs)
	case MethodNpm:
		return npmLike("npm", []string{"install", "-g", "--no-fund", "--no-audit"}, spec, obs)
	case MethodBun:
		return npmLike("bun", []string{"install", "-g"}, spec, obs)
	case MethodPnpm:
		return npmLike("pnpm", []string{"add", "-g"}, spec, obs)
	case MethodYarn:
		return npmLike("yarn", []string{"global", "add"}, spec, obs)
	case MethodNative:
		if len(spec.Native) == 0 {
			return nil, fmt.Errorf("no upgrade recipe")
		}
		for _, a := range spec.Native {
			if !safeArg(a) {
				return nil, fmt.Errorf("refusing unsafe native arg %q", a)
			}
		}
		return &Command{Argv: append([]string(nil), spec.Native...), Method: MethodNative, Display: shellquote.Display(spec.Native)}, nil
	default:
		return nil, fmt.Errorf("unknown method %q", method)
	}
}

func brewCommand(spec Spec, obs Observation) (*Command, error) {
	token := obs.BrewToken
	kind := obs.BrewKind
	if token == "" {
		token = spec.Brew
		if kind == "" {
			kind = spec.BrewKind
		}
	}
	if token == "" {
		return nil, fmt.Errorf("no upgrade recipe")
	}
	if !safeToken(token) {
		return nil, fmt.Errorf("refusing unsafe brew token %q", token)
	}
	if kind == "" && token == spec.Brew {
		kind = spec.BrewKind
	}
	var argv []string
	switch kind {
	case "cask":
		argv = []string{"brew", "upgrade", "--cask", token}
	case "formula", "":
		argv = []string{"brew", "upgrade", token}
	default:
		return nil, fmt.Errorf("unknown brew kind %q", kind)
	}
	storedKind := kind
	if storedKind == "" {
		storedKind = "formula"
	}
	return &Command{
		Argv: argv, Method: MethodBrew, Display: shellquote.Display(argv),
		BrewToken: token, BrewKind: storedKind,
	}, nil
}

func npmLike(bin string, prefix []string, spec Spec, obs Observation) (*Command, error) {
	pkg := obs.Package
	if pkg == "" {
		pkg = inferPackage(obs.Real)
	}
	if pkg == "" {
		pkg = inferPackage(obs.Path)
	}
	if pkg == "" {
		pkg = spec.Npm
	}
	if pkg == "" {
		// OpenCode's own upgrader detects npm, pnpm, and bun. Prefer that
		// over guessing opencode-ai versus @opencode/cli.
		if len(spec.Native) > 0 && (bin == "npm" || bin == "bun" || bin == "pnpm") {
			for _, a := range spec.Native {
				if !safeArg(a) {
					return nil, fmt.Errorf("refusing unsafe native arg %q", a)
				}
			}
			return &Command{Argv: append([]string(nil), spec.Native...), Method: MethodNative, Display: shellquote.Display(spec.Native)}, nil
		}
		return nil, fmt.Errorf("no upgrade recipe")
	}
	if !safePackage(pkg) {
		return nil, fmt.Errorf("refusing unsafe package %q", pkg)
	}
	argv := append(append([]string{}, bin), prefix...)
	argv = append(argv, pkg+"@latest")
	return &Command{Argv: argv, Method: bin, Display: shellquote.Display(argv), Package: pkg}, nil
}

func inferPackage(path string) string {
	known := []string{
		"@anthropic-ai/claude-code",
		"@openai/codex",
		"@opencode/cli",
		"opencode-ai",
		"@google/gemini-cli",
		"@github/copilot",
		"@ampcode/cli",
		"@sourcegraph/amp",
		"@earendil-works/pi-coding-agent",
		"@qwen-code/qwen-code",
		"@moonshot-ai/kimi-code",
		"@deepseek-ai/dsh",
		"@xai-official/grok",
		"@kilocode/cli",
	}
	for _, pkg := range known {
		if strings.Contains(path, pkg) {
			return pkg
		}
	}
	return ""
}

var (
	tokenRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_@+.-]{0,127}$`)
	pkgRe   = regexp.MustCompile(`^(@[A-Za-z0-9._-]+/)?[A-Za-z0-9._-]{1,128}$`)
	argRe   = regexp.MustCompile(`^[A-Za-z0-9_@+./:=,-]{1,128}$`)
)

func safeToken(s string) bool { return tokenRe.MatchString(s) && !strings.Contains(s, "..") }
func safePackage(s string) bool {
	if !pkgRe.MatchString(s) || strings.Contains(s, "..") {
		return false
	}
	// npm would treat a leading dash as a flag. A name or scope segment
	// that starts with "-" or "." is not a real package name.
	name := s
	if i := strings.LastIndex(s, "/"); i >= 0 {
		name = s[i+1:]
	}
	return name != "" && name[0] != '-' && name[0] != '.'
}
func safeArg(s string) bool { return argRe.MatchString(s) && !strings.Contains(s, "..") }

// PackageOK reports whether s is safe to splice into an npm pack command.
func PackageOK(s string) bool { return safePackage(s) }

// BrewTokenOK reports whether s is safe to pass to brew fetch or brew upgrade.
func BrewTokenOK(s string) bool { return safeToken(s) }

// BrewKindOK reports whether kind is a Homebrew target we know how to stage.
func BrewKindOK(kind string) bool { return kind == "cask" || kind == "formula" }

func setOf(in []string) map[string]struct{} {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(in))
	for _, s := range in {
		s = strings.ToLower(strings.TrimSpace(s))
		if s != "" {
			out[s] = struct{}{}
		}
	}
	return out
}
