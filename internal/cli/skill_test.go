package cli

import (
	"bytes"
	"os"
	"regexp"
	"strings"
	"testing"
)

// rootCommands lists the commands in the root help's Commands section.
func rootCommands(t *testing.T) map[string]bool {
	t.Helper()
	cmds := map[string]bool{}
	_, rest, ok := strings.Cut(rootHelp, "Commands:\n")
	if !ok {
		t.Fatal("root help has no Commands section")
	}
	for _, line := range strings.Split(rest, "\n") {
		if !strings.HasPrefix(line, "  ") {
			break
		}
		cmds[strings.Fields(line)[0]] = true
	}
	return cmds
}

func commandHelp(t *testing.T, cmd string) string {
	t.Helper()
	var out, errBuf bytes.Buffer
	if code := Execute([]string{cmd, "--help"}, &out, &errBuf); code != 0 {
		t.Fatalf("clifleet %s --help exit %d\n%s", cmd, code, errBuf.String())
	}
	return out.String() + errBuf.String()
}

// TestSkillNamesOnlyRealCommandsAndFlags keeps skills/clifleet/SKILL.md in
// step with the CLI: every command it names is in the root help, and every
// flag it names is in that command's --help.
func TestSkillNamesOnlyRealCommandsAndFlags(t *testing.T) {
	raw, err := os.ReadFile("../../skills/clifleet/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)

	front, _, ok := strings.Cut(strings.TrimPrefix(text, "---\n"), "\n---\n")
	if !strings.HasPrefix(text, "---\n") || !ok {
		t.Fatal("SKILL.md needs YAML front matter")
	}
	var name, desc string
	for _, line := range strings.Split(front, "\n") {
		if v, ok := strings.CutPrefix(line, "name: "); ok {
			name = v
		}
		if v, ok := strings.CutPrefix(line, "description: "); ok {
			desc = v
		}
	}
	if name != "clifleet" || desc == "" || len(desc) > 300 {
		t.Fatalf("front matter name %q, description %d chars (want 1..300)", name, len(desc))
	}

	cmds := rootCommands(t)
	helps := map[string]string{}
	help := func(cmd string) string {
		if _, ok := helps[cmd]; !ok {
			helps[cmd] = commandHelp(t, cmd)
		}
		return helps[cmd]
	}
	var anyHelp strings.Builder
	for cmd := range cmds {
		anyHelp.WriteString(help(cmd))
	}

	spans := regexp.MustCompile("`([^`\n]+)`").FindAllStringSubmatch(text, -1)
	checked := 0
	for _, m := range spans {
		fields := strings.Fields(m[1])
		explicit := fields[0] == "clifleet"
		if explicit {
			fields = fields[1:]
			if len(fields) == 0 {
				continue
			}
		}
		cmd := fields[0]
		switch {
		case cmds[cmd]:
			for _, f := range fields[1:] {
				if strings.HasPrefix(f, "--") && !hasFlag(help(cmd), f) {
					t.Errorf("SKILL.md `%s`: %s is not in clifleet %s --help", m[1], f, cmd)
				}
			}
			checked++
		case explicit:
			t.Errorf("SKILL.md `%s`: %q is not a clifleet command", m[1], cmd)
		case strings.HasPrefix(cmd, "--"):
			for _, f := range fields {
				if strings.HasPrefix(f, "--") && !hasFlag(anyHelp.String(), f) {
					t.Errorf("SKILL.md `%s`: %s is in no command's --help", m[1], f)
				}
			}
			checked++
		}
	}
	if checked < 10 {
		t.Fatalf("only %d command spans checked; the parser no longer matches SKILL.md", checked)
	}
}

// hasFlag matches flag as a whole word, so --host does not pass on --hosts.
func hasFlag(help, flag string) bool {
	return regexp.MustCompile(`(^|[^\w-])` + regexp.QuoteMeta(flag) + `([^\w-]|$)`).MatchString(help)
}
