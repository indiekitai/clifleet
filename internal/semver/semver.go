// Package semver orders release versions the way clifleet compares them:
// with or without a leading v, pre-releases before their release.
package semver

import (
	"strconv"
	"strings"
)

// Compare orders two semantic versions, with or without a leading v. ok is
// false when either is not one, such as "dev". Go pseudo-versions
// (v0.1.1-0.20261029105244-de69aaed22aa) sort correctly against tags.
func Compare(a, b string) (int, bool) {
	va, ok := parse(a)
	if !ok {
		return 0, false
	}
	vb, ok := parse(b)
	if !ok {
		return 0, false
	}
	for i := 0; i < 3; i++ {
		if va.core[i] != vb.core[i] {
			if va.core[i] < vb.core[i] {
				return -1, true
			}
			return 1, true
		}
	}
	return comparePre(va.pre, vb.pre), true
}

type version struct {
	core [3]int
	pre  []string
}

func parse(s string) (version, bool) {
	var v version
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	core, pre, hasPre := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p == "" {
			return v, false
		}
		v.core[i] = n
	}
	if hasPre {
		if pre == "" {
			return v, false
		}
		v.pre = strings.Split(pre, ".")
	}
	return v, true
}

// comparePre follows semver: a release sorts after its pre-releases, numeric
// identifiers compare as numbers and before alphanumeric ones.
func comparePre(a, b []string) int {
	switch {
	case len(a) == 0 && len(b) == 0:
		return 0
	case len(a) == 0:
		return 1
	case len(b) == 0:
		return -1
	}
	for i := 0; i < len(a) && i < len(b); i++ {
		na, ea := strconv.Atoi(a[i])
		nb, eb := strconv.Atoi(b[i])
		switch {
		case ea == nil && eb == nil:
			if na != nb {
				if na < nb {
					return -1
				}
				return 1
			}
		case ea == nil:
			return -1
		case eb == nil:
			return 1
		default:
			if c := strings.Compare(a[i], b[i]); c != 0 {
				return c
			}
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return 0
}
