package shellquote

import "testing"

func TestQuote(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"claude", "'claude'"},
		{"", "''"},
		{"a'b", `'a'\''b'`},
		{"claude-code@latest", "'claude-code@latest'"},
		{"has space", "'has space'"},
	}
	for _, tc := range cases {
		if got := Quote(tc.in); got != tc.want {
			t.Errorf("Quote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDisplayBarePackage(t *testing.T) {
	got := Display([]string{"npm", "install", "-g", "@openai/codex@latest"})
	want := "npm install -g @openai/codex@latest"
	if got != want {
		t.Fatalf("got %q", got)
	}
}

func TestFormatAlwaysQuotes(t *testing.T) {
	got := Format([]string{"claude", "update"})
	if got != "'claude' 'update'" {
		t.Fatalf("got %q", got)
	}
}
