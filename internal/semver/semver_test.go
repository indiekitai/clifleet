package semver

import "testing"

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
		ok   bool
	}{
		{"v0.2.0", "v0.2.0", 0, true},
		{"0.1.0", "v0.2.0", -1, true},
		{"v0.10.0", "v0.9.0", 1, true},
		{"v0.1.1-0.20261029105244-de69aaed22aa", "v0.2.0", -1, true},
		{"v0.2.1-0.20261030000000-abcdef123456", "v0.2.0", 1, true},
		{"v0.2.0-rc.1", "v0.2.0", -1, true},
		{"v0.2.0-rc.2", "v0.2.0-rc.10", -1, true},
		{"v0.2.0-rc.10", "v0.2.0-rc.2", 1, true},
		{"v0.2.0-1", "v0.2.0-rc", -1, true},
		{"v0.2.0+dirty", "v0.2.0", 0, true},
		{"dev", "v0.2.0", 0, false},
		{"(devel)", "v0.2.0", 0, false},
		{"v1.2", "v0.2.0", 0, false},
	}
	for _, c := range cases {
		got, ok := Compare(c.a, c.b)
		if got != c.want || ok != c.ok {
			t.Errorf("Compare(%q, %q) = %d, %v; want %d, %v", c.a, c.b, got, ok, c.want, c.ok)
		}
	}
}
