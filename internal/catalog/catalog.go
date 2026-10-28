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
type Command struct {
	Argv    []string
	Method  string
	Display string
}

// Decision is the per-tool plan. Command is nil when nothing should run.
type Decision struct {
	Method  string
	Command *Command
	Skip    string // human reason; empty when a command will run
	Fail    string // hard failure; nothing is run
}

// DefaultTools are the candidates clifleet looks for unless the fleet file
// says otherwise. Regional tools are intentionally absent.
func DefaultTools() []string {
	return []string{"claude", "codex", "opencode"}
}

// Builtin specs match the upstream install channels as of October 2026.
// Native updaters: `claude update`, `codex update`, `opencode upgrade`.
// Package-manager installs are upgraded with that package manager, because
// the native updater is the wrong command for a Homebrew cask or an npm global.
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
	return &Command{Argv: argv, Method: MethodBrew, Display: shellquote.Display(argv)}, nil
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
	return &Command{Argv: argv, Method: bin, Display: shellquote.Display(argv)}, nil
}

func inferPackage(path string) string {
	known := []string{
		"@anthropic-ai/claude-code",
		"@openai/codex",
		"@opencode/cli",
		"opencode-ai",
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
	return pkgRe.MatchString(s) && !strings.Contains(s, "..")
}
func safeArg(s string) bool { return argRe.MatchString(s) && !strings.Contains(s, "..") }

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
