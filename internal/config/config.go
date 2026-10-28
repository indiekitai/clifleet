// Package config loads a clifleet fleet file.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const (
	DefaultConcurrency = 8
	MaxConcurrency     = 64
	DefaultTimeout     = 3 * time.Minute
	MinTimeout         = time.Second
	MaxTimeout         = 30 * time.Minute
)

// File is ~/.config/clifleet/hosts.toml.
//
// Tools is a candidate list. clifleet looks for those binaries and upgrades
// only the ones it finds. It never installs a candidate that is missing.
type File struct {
	Tools       []string `toml:"tools"`
	Concurrency int      `toml:"concurrency"`
	Timeout     string   `toml:"timeout"`
	Hosts       []Host   `toml:"hosts"`
	Recipe      []Recipe `toml:"recipe"`
}

// Host is one machine. SSH is an OpenSSH destination: a config alias
// ("claw1"), a host, or user@host. An optional :port suffix is accepted.
type Host struct {
	Name      string            `toml:"name"`
	SSH       string            `toml:"ssh"`
	Host      string            `toml:"host"`
	User      string            `toml:"user"`
	Port      int               `toml:"port"`
	Identity  string            `toml:"identity"`
	Local     bool              `toml:"local"`
	Tools     []string          `toml:"tools"`
	Skip      []string          `toml:"skip"`
	PathExtra []string          `toml:"path_extra"`
	Methods   map[string]string `toml:"methods"`
}

// Recipe teaches clifleet how to upgrade an already-installed tool.
// A recipe never adds the tool to the candidate list and never installs it.
type Recipe struct {
	Name     string   `toml:"name"`
	Binary   string   `toml:"binary"`
	Native   []string `toml:"native"`
	Npm      string   `toml:"npm"`
	Brew     string   `toml:"brew"`
	BrewKind string   `toml:"brew_kind"`
}

// Loaded is a validated fleet file plus the path it came from.
type Loaded struct {
	Path        string
	Tools       []string
	Concurrency int
	Timeout     time.Duration
	Hosts       []Host
	Recipes     []Recipe
}

var (
	nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
	pathRe = regexp.MustCompile(`^/[A-Za-z0-9._@+/-]+$`)
)

// DefaultTools is the candidate set used when the file omits tools.
func DefaultTools() []string {
	return []string{"claude", "codex", "opencode"}
}

// DefaultPath is $XDG_CONFIG_HOME/clifleet/hosts.toml or ~/.config/clifleet/hosts.toml.
func DefaultPath() (string, error) {
	if x := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); x != "" {
		return filepath.Join(x, "clifleet", "hosts.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home directory: %w", err)
	}
	return filepath.Join(home, ".config", "clifleet", "hosts.toml"), nil
}

// Load reads and validates a fleet file.
func Load(path string) (Loaded, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Loaded{}, err
	}
	return Parse(path, data)
}

// Parse validates TOML bytes. Unknown keys are errors.
func Parse(path string, data []byte) (Loaded, error) {
	var raw File
	md, err := toml.Decode(string(data), &raw)
	if err != nil {
		return Loaded{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if undec := md.Undecoded(); len(undec) > 0 {
		parts := make([]string, len(undec))
		for i, k := range undec {
			parts[i] = k.String()
		}
		return Loaded{}, fmt.Errorf("parse %s: unknown field %s", path, strings.Join(parts, ", "))
	}
	return normalize(path, raw)
}

func normalize(path string, raw File) (Loaded, error) {
	tools := normalizeNames(raw.Tools)
	if len(raw.Tools) == 0 {
		tools = DefaultTools()
	}
	if len(tools) == 0 {
		return Loaded{}, errors.New("tools list is empty")
	}
	for _, name := range tools {
		if err := checkName("tool", name); err != nil {
			return Loaded{}, err
		}
	}

	concurrency := raw.Concurrency
	if concurrency == 0 {
		concurrency = DefaultConcurrency
	}
	if concurrency < 1 || concurrency > MaxConcurrency {
		return Loaded{}, fmt.Errorf("concurrency must be 1..%d", MaxConcurrency)
	}

	timeout := DefaultTimeout
	if strings.TrimSpace(raw.Timeout) != "" {
		d, err := ParseDuration(raw.Timeout)
		if err != nil {
			return Loaded{}, fmt.Errorf("timeout: %w", err)
		}
		if d < MinTimeout || d > MaxTimeout {
			return Loaded{}, fmt.Errorf("timeout must be between %s and %s", MinTimeout, MaxTimeout)
		}
		timeout = d
	}

	if len(raw.Hosts) == 0 {
		return Loaded{}, errors.New("no hosts defined")
	}

	seen := map[string]struct{}{}
	var errs []error
	hosts := make([]Host, len(raw.Hosts))
	for i, h := range raw.Hosts {
		h.Name = strings.TrimSpace(h.Name)
		if err := checkName("host", h.Name); err != nil {
			errs = append(errs, err)
			continue
		}
		key := strings.ToLower(h.Name)
		if _, ok := seen[key]; ok {
			errs = append(errs, fmt.Errorf("duplicate host %q", h.Name))
			continue
		}
		seen[key] = struct{}{}

		h.Tools = normalizeNames(h.Tools)
		h.Skip = normalizeNames(h.Skip)
		for _, name := range append(append([]string{}, h.Tools...), h.Skip...) {
			if err := checkName("tool", name); err != nil {
				errs = append(errs, fmt.Errorf("host %s: %w", h.Name, err))
			}
		}
		if len(h.Methods) > 0 {
			methods := make(map[string]string, len(h.Methods))
			for tool, method := range h.Methods {
				n := strings.ToLower(strings.TrimSpace(tool))
				if err := checkName("tool", n); err != nil {
					errs = append(errs, fmt.Errorf("host %s: %w", h.Name, err))
					continue
				}
				method = strings.ToLower(strings.TrimSpace(method))
				if !validMethod(method) {
					errs = append(errs, fmt.Errorf("host %s: method %q for %s is not one of brew, npm, bun, pnpm, yarn, native", h.Name, method, n))
					continue
				}
				methods[n] = method
			}
			h.Methods = methods
		}

		if h.Port < 0 || h.Port > 65535 {
			errs = append(errs, fmt.Errorf("host %s: invalid port %d", h.Name, h.Port))
		}
		if h.Identity != "" {
			expanded, err := ExpandHome(h.Identity)
			if err != nil {
				errs = append(errs, fmt.Errorf("host %s: identity: %w", h.Name, err))
			} else {
				h.Identity = expanded
			}
		}
		for _, p := range h.PathExtra {
			if !pathRe.MatchString(p) || strings.Contains(p, "..") {
				errs = append(errs, fmt.Errorf("host %s: path_extra %q must be an absolute path without spaces or ..", h.Name, p))
			}
		}
		if _, _, err := Destination(h); err != nil {
			errs = append(errs, err)
		}
		hosts[i] = h
	}
	if len(errs) > 0 {
		return Loaded{}, errors.Join(errs...)
	}

	recipes := make([]Recipe, len(raw.Recipe))
	for i, r := range raw.Recipe {
		r.Name = strings.ToLower(strings.TrimSpace(r.Name))
		if err := checkName("recipe", r.Name); err != nil {
			return Loaded{}, err
		}
		if r.Binary != "" {
			r.Binary = strings.ToLower(strings.TrimSpace(r.Binary))
			if err := checkName("binary", r.Binary); err != nil {
				return Loaded{}, fmt.Errorf("recipe %s: %w", r.Name, err)
			}
		}
		if r.BrewKind != "" && r.BrewKind != "cask" && r.BrewKind != "formula" {
			return Loaded{}, fmt.Errorf("recipe %s: brew_kind must be cask or formula", r.Name)
		}
		recipes[i] = r
	}

	return Loaded{
		Path:        path,
		Tools:       tools,
		Concurrency: concurrency,
		Timeout:     timeout,
		Hosts:       hosts,
		Recipes:     recipes,
	}, nil
}

// Destination is the OpenSSH destination and the port flag to use.
// An explicit port field wins over a :port suffix. Conflicts are errors.
func Destination(h Host) (dest string, port int, err error) {
	if h.Local {
		return "local", 0, nil
	}
	port = h.Port
	if s := strings.TrimSpace(h.SSH); s != "" {
		d, p, err := ParseSSH(s)
		if err != nil {
			return "", 0, fmt.Errorf("host %s: %w", h.Name, err)
		}
		if p != 0 && h.Port != 0 && p != h.Port {
			return "", 0, fmt.Errorf("host %s: ssh destination port %d conflicts with port = %d", h.Name, p, h.Port)
		}
		if port == 0 {
			port = p
		}
		return d, port, nil
	}
	host := strings.TrimSpace(h.Host)
	if host == "" {
		return "", 0, fmt.Errorf("host %s: set ssh, host, or local = true", h.Name)
	}
	if strings.ContainsAny(host, " \t'\"") {
		return "", 0, fmt.Errorf("host %s: invalid host %q", h.Name, host)
	}
	user := strings.TrimSpace(h.User)
	if user != "" {
		if strings.ContainsAny(user, " \t'\"@:") {
			return "", 0, fmt.Errorf("host %s: invalid user %q", h.Name, user)
		}
		return user + "@" + host, port, nil
	}
	return host, port, nil
}

// ParseSSH splits an OpenSSH destination. A single :port suffix is accepted.
// IPv6 literals belong in ~/.ssh/config; use the alias as ssh.
func ParseSSH(s string) (dest string, port int, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", 0, errors.New("empty ssh destination")
	}
	if strings.ContainsAny(s, " \t'\"") {
		return "", 0, fmt.Errorf("invalid ssh destination %q", s)
	}
	hostpart := s
	prefix := ""
	if at := strings.LastIndex(s, "@"); at >= 0 {
		if at == 0 || at == len(s)-1 {
			return "", 0, fmt.Errorf("invalid ssh destination %q", s)
		}
		prefix = s[:at+1]
		hostpart = s[at+1:]
	}
	if strings.Count(hostpart, ":") > 1 {
		return "", 0, fmt.Errorf("invalid ssh destination %q (use an ssh config alias for IPv6)", s)
	}
	if c := strings.LastIndex(hostpart, ":"); c >= 0 {
		pstr := hostpart[c+1:]
		p, convErr := strconv.Atoi(pstr)
		if convErr != nil || p < 1 || p > 65535 {
			return "", 0, fmt.Errorf("invalid port in ssh destination %q", s)
		}
		if c == 0 {
			return "", 0, fmt.Errorf("invalid ssh destination %q", s)
		}
		return prefix + hostpart[:c], p, nil
	}
	return s, 0, nil
}

// ParseDuration parses a Go duration. A bare integer is seconds.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty duration")
	}
	allDigits := true
	for _, r := range s {
		if r < '0' || r > '9' {
			allDigits = false
			break
		}
	}
	if allDigits {
		n, err := strconv.Atoi(s)
		if err != nil {
			return 0, err
		}
		return time.Duration(n) * time.Second, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("parse %q: %w", s, err)
	}
	return d, nil
}

// ExpandHome expands a leading ~/ to the user home directory.
func ExpandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

// FilterHosts keeps hosts named by --hosts. Names match case-insensitively.
// An ssh alias matches exactly. The result preserves the requested order.
func FilterHosts(hosts []Host, want []string) ([]Host, error) {
	if len(want) == 0 {
		return hosts, nil
	}
	var out []Host
	var missing []string
	for _, w := range want {
		w = strings.TrimSpace(w)
		if w == "" {
			continue
		}
		var found *Host
		for i := range hosts {
			if strings.EqualFold(hosts[i].Name, w) || hosts[i].SSH == w {
				h := hosts[i]
				found = &h
				break
			}
		}
		if found == nil {
			missing = append(missing, w)
			continue
		}
		out = append(out, *found)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("unknown host %s", strings.Join(missing, ", "))
	}
	if len(out) == 0 {
		return nil, errors.New("no hosts selected")
	}
	return out, nil
}

func normalizeNames(in []string) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, name := range in {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func checkName(kind, name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid %s name %q", kind, name)
	}
	return nil
}

func validMethod(m string) bool {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case "brew", "npm", "bun", "pnpm", "yarn", "native":
		return true
	default:
		return false
	}
}
