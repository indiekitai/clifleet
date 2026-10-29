package config

import (
	"strings"
	"testing"

	"github.com/indiekitai/clifleet/internal/catalog"
)

func TestExampleParses(t *testing.T) {
	cfg, err := Parse("hosts.toml", []byte(Example))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.Tools, ",") != strings.Join(catalog.DefaultTools(), ",") {
		t.Fatalf("tools = %v", cfg.Tools)
	}
	if strings.Join(DefaultTools(), ",") != strings.Join(catalog.DefaultTools(), ",") {
		t.Fatalf("config defaults drifted from catalog: %v", DefaultTools())
	}
	if cfg.Concurrency != 8 || cfg.Timeout.String() != "3m0s" || !cfg.TimeoutExplicit {
		t.Fatalf("concurrency %d timeout %s explicit %v", cfg.Concurrency, cfg.Timeout, cfg.TimeoutExplicit)
	}
	if len(cfg.Hosts) != 4 {
		t.Fatalf("hosts %d", len(cfg.Hosts))
	}
	byName := map[string]Host{}
	for _, h := range cfg.Hosts {
		byName[h.Name] = h
	}
	us := byName["us-server"]
	if us.SSH != "claw1" {
		t.Fatalf("ssh %q", us.SSH)
	}
	if strings.Join(us.Tools, ",") != "claude,codex" {
		t.Fatalf("us allowlist %v", us.Tools)
	}
	sh := byName["shanghai"]
	if strings.Join(sh.Tools, ",") != "claude,opencode,dsh" {
		t.Fatalf("shanghai %v", sh.Tools)
	}
	if strings.Join(byName["workshop"].Skip, ",") != "opencode" {
		t.Fatalf("skip %v", byName["workshop"].Skip)
	}
	if !byName["laptop"].Local {
		t.Fatal("laptop should be local")
	}
	for _, name := range cfg.Tools {
		if name == "dsh" {
			t.Fatal("dsh must not be a global candidate")
		}
	}
}

func TestOmittedToolsUsesCatalogDefault(t *testing.T) {
	cfg, err := Parse("hosts.toml", []byte(`
[[hosts]]
name = "a"
local = true
`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.Tools, ",") != strings.Join(catalog.DefaultTools(), ",") {
		t.Fatalf("omitted tools = %v", cfg.Tools)
	}
	for _, name := range cfg.Tools {
		switch name {
		case "dsh", "grok", "kilo", "kiro-cli":
			t.Fatalf("%s must not be a default candidate", name)
		}
	}
}

func TestUnknownField(t *testing.T) {
	_, err := Parse("hosts.toml", []byte(`
tools = ["claude"]
[[hosts]]
name = "a"
local = true
tolls = ["claude"]
`))
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("got %v", err)
	}
}

func TestMethodsAndPort(t *testing.T) {
	cfg, err := Parse("hosts.toml", []byte(`
tools = ["Claude", "codex"]
concurrency = 4
timeout = "90s"

[[hosts]]
name = "box"
ssh = "ada@10.0.0.8:2222"
identity = "~/keys/id"
path_extra = ["/opt/custom/bin"]

[hosts.methods]
Claude = "npm"
`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.Tools, ",") != "claude,codex" {
		t.Fatalf("tools %v", cfg.Tools)
	}
	h := cfg.Hosts[0]
	if h.Methods["claude"] != "npm" {
		t.Fatalf("methods %#v", h.Methods)
	}
	dest, port, err := Destination(h)
	if err != nil {
		t.Fatal(err)
	}
	if dest != "ada@10.0.0.8" || port != 2222 {
		t.Fatalf("dest %s port %d", dest, port)
	}
	if !strings.HasSuffix(h.Identity, "/keys/id") || strings.HasPrefix(h.Identity, "~") {
		t.Fatalf("identity %s", h.Identity)
	}
}

func TestRejects(t *testing.T) {
	cases := map[string]string{
		"no hosts":    "tools = [\"claude\"]\n",
		"bad method":  "[[hosts]]\nname = \"a\"\nlocal = true\n[hosts.methods]\nclaude = \"curl\"\n",
		"dup host":    "[[hosts]]\nname = \"a\"\nlocal = true\n[[hosts]]\nname = \"A\"\nlocal = true\n",
		"port clash":  "[[hosts]]\nname = \"a\"\nssh = \"h:22\"\nport = 2222\n",
		"bad path":    "[[hosts]]\nname = \"a\"\nlocal = true\npath_extra = [\"../x\"]\n",
		"empty ssh":   "[[hosts]]\nname = \"a\"\nssh = \"\"\n",
		"bad timeout": "timeout = \"nope\"\n[[hosts]]\nname = \"a\"\nlocal = true\n",
		"dash ssh":    "[[hosts]]\nname = \"a\"\nssh = \"-oProxyCommand=touch/tmp/x\"\n",
		"dash host":   "[[hosts]]\nname = \"a\"\nhost = \"-oProxyCommand=x\"\n",
		"dash user":   "[[hosts]]\nname = \"a\"\nhost = \"h\"\nuser = \"-oProxyCommand=x\"\n",
		"same ssh":    "[[hosts]]\nname = \"a\"\nssh = \"box\"\n[[hosts]]\nname = \"b\"\nssh = \"box\"\n",
		"two local":   "[[hosts]]\nname = \"a\"\nlocal = true\n[[hosts]]\nname = \"b\"\nlocal = true\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse("hosts.toml", []byte(body)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestParseSSH(t *testing.T) {
	dest, port, err := ParseSSH("claw1")
	if err != nil || dest != "claw1" || port != 0 {
		t.Fatalf("%s %d %v", dest, port, err)
	}
	dest, port, err = ParseSSH("dev@host:2222")
	if err != nil || dest != "dev@host" || port != 2222 {
		t.Fatalf("%s %d %v", dest, port, err)
	}
	if _, _, err := ParseSSH("has space"); err == nil {
		t.Fatal("expected error")
	}
	if _, _, err := ParseSSH("::1"); err == nil {
		t.Fatal("expected ipv6 error")
	}
}

func TestFilterHosts(t *testing.T) {
	hosts := []Host{{Name: "us-server", SSH: "claw1"}, {Name: "workshop", SSH: "workshop"}}
	got, err := FilterHosts(hosts, []string{"US-SERVER"})
	if err != nil || len(got) != 1 || got[0].Name != "us-server" {
		t.Fatalf("%v %v", got, err)
	}
	got, err = FilterHosts(hosts, []string{"claw1"})
	if err != nil || len(got) != 1 || got[0].Name != "us-server" {
		t.Fatalf("alias %v %v", got, err)
	}
	// SSH alias match is exact; the name match above is case-insensitive.
	got, err = FilterHosts(hosts, []string{"workshop"})
	if err != nil || got[0].Name != "workshop" {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := FilterHosts(hosts, []string{"missing"}); err == nil {
		t.Fatal("expected error")
	}
	// The same host named twice, or by name and alias, runs once.
	got, err = FilterHosts(hosts, []string{"us-server", "claw1", "US-SERVER", "workshop"})
	if err != nil || len(got) != 2 || got[0].Name != "us-server" || got[1].Name != "workshop" {
		t.Fatalf("dedup %v %v", got, err)
	}
}

func TestParseDurationBareSeconds(t *testing.T) {
	d, err := ParseDuration("45")
	if err != nil || d.String() != "45s" {
		t.Fatalf("%s %v", d, err)
	}
}

// An ssh alias named "local" is another machine, not the control host.
func TestLocalAndAnSSHAliasNamedLocalAreDifferentMachines(t *testing.T) {
	body := "[[hosts]]\nname = \"here\"\nlocal = true\n[[hosts]]\nname = \"there\"\nssh = \"local\"\n"
	if _, err := Parse("hosts.toml", []byte(body)); err != nil {
		t.Fatal(err)
	}
}
