package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

const tag = "v9.9.9"

var assetName = "clifleet_" + runtime.GOOS + "_" + runtime.GOARCH

// fakeBinary stands in for a release build: it answers version with says.
func fakeBinary(says string) []byte {
	return []byte("#!/bin/sh\necho '" + says + "'\n")
}

func tarball(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sumsFor(archive []byte, name string) []byte {
	sum := sha256.Sum256(archive)
	return []byte(hex.EncodeToString(sum[:]) + "  " + name + ".tar.gz\n")
}

type release struct {
	archive []byte
	sums    []byte
	mu      sync.Mutex
	paths   []string
}

func newRelease(t *testing.T, says string) *release {
	archive := tarball(t, assetName, fakeBinary(says))
	return &release{archive: archive, sums: sumsFor(archive, assetName)}
}

func (r *release) serve(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.paths = append(r.paths, req.URL.Path)
		r.mu.Unlock()
		switch req.URL.Path {
		case "/releases/latest":
			http.Redirect(w, req, "/releases/tag/"+tag, http.StatusFound)
		case "/releases/download/" + tag + "/SHA256SUMS":
			w.Write(r.sums)
		case "/releases/download/" + tag + "/" + assetName + ".tar.gz":
			w.Write(r.archive)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(srv.Close)
	old := BaseURL
	BaseURL = srv.URL + "/releases"
	t.Cleanup(func() { BaseURL = old })
}

func (r *release) fetched(file string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.paths {
		if strings.HasSuffix(p, "/"+file) {
			return true
		}
	}
	return false
}

func target(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "clifleet")
	if err := os.WriteFile(p, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func opts(current, path string) Options {
	return Options{Current: current, Target: path, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
}

// unchanged fails the test if path is no longer the old binary or the
// update left a temp file next to it.
func unchanged(t *testing.T, path string) {
	t.Helper()
	if got, _ := os.ReadFile(path); string(got) != "old" {
		t.Fatalf("the binary was replaced: %q", got)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("left files behind: %v", entries)
	}
}

func TestUpdateReplacesWithANewerRelease(t *testing.T) {
	rel := newRelease(t, "clifleet "+tag)
	rel.serve(t)
	path := target(t)
	res, err := Update(context.Background(), opts("v0.1.1-0.20261029105244-de69aaed22aa", path))
	if err != nil || !res.Updated || res.Latest != tag {
		t.Fatalf("%+v %v", res, err)
	}
	out, err := exec.Command(path, "version").Output()
	if err != nil || strings.TrimSpace(string(out)) != "clifleet "+tag {
		t.Fatalf("%q %v", out, err)
	}
}

func TestUpdateRefusesAnArchiveThatFailsItsChecksum(t *testing.T) {
	rel := newRelease(t, "clifleet "+tag)
	rel.sums = sumsFor([]byte("something else"), assetName)
	rel.serve(t)
	path := target(t)
	if _, err := Update(context.Background(), opts("v0.1.0", path)); err == nil || !strings.Contains(err.Error(), "SHA256SUMS") {
		t.Fatal(err)
	}
	unchanged(t, path)
}

func TestUpdateRefusesABuildThatDoesNotReportTheTag(t *testing.T) {
	// A build for another platform, or a broken one, never answers with the tag.
	rel := newRelease(t, "clifleet v1.0.0")
	rel.serve(t)
	path := target(t)
	if _, err := Update(context.Background(), opts("v0.1.0", path)); err == nil || !strings.Contains(err.Error(), "does not run here") {
		t.Fatal(err)
	}
	unchanged(t, path)
}

func TestUpdateLeavesACurrentRelease(t *testing.T) {
	rel := newRelease(t, "clifleet "+tag)
	rel.serve(t)
	path := target(t)
	res, err := Update(context.Background(), opts(tag, path))
	if err != nil || res.Updated || res.Available {
		t.Fatalf("%+v %v", res, err)
	}
	if rel.fetched(assetName + ".tar.gz") {
		t.Fatal("downloaded an archive it did not need")
	}
	unchanged(t, path)
}

func TestUpdateLeavesASourceBuildUnlessForced(t *testing.T) {
	rel := newRelease(t, "clifleet "+tag)
	rel.serve(t)
	path := target(t)
	if _, err := Update(context.Background(), opts("dev", path)); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatal(err)
	}
	unchanged(t, path)
	check := opts("dev", path)
	check.Check = true
	if res, err := Update(context.Background(), check); err != nil || !res.Available || res.Updated {
		t.Fatalf("%+v %v", res, err)
	}
	unchanged(t, path)
	force := opts("dev", path)
	force.Force = true
	if res, err := Update(context.Background(), force); err != nil || !res.Updated {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestUpdateRejectsAPlatformWithoutABuild(t *testing.T) {
	o := opts("v0.1.0", target(t))
	o.GOOS = "windows"
	if _, err := Update(context.Background(), o); err == nil || !strings.Contains(err.Error(), "windows") {
		t.Fatal(err)
	}
}

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

// fakeCurl serves install.sh from a fixtures directory instead of GitHub.
const fakeCurl = `#!/bin/sh
out=
fmt=
url=
while [ $# -gt 0 ]; do
  case $1 in
    -o) out=$2; shift ;;
    -w) fmt=$2; shift ;;
    -*) ;;
    *) url=$1 ;;
  esac
  shift
done
printf '%s\n' "$url" >>"$FAKE_LOG"
if [ -n "$fmt" ]; then
  printf 'https://github.com/indiekitai/clifleet/releases/tag/v9.9.9'
  exit 0
fi
f=$FIXTURES/${url##*/}
[ -f "$f" ] || exit 22
cp "$f" "$out"
`

func runInstall(t *testing.T, rel *release, env ...string) (string, string, error) {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	fixtures := filepath.Join(root, "fixtures")
	home := filepath.Join(root, "home")
	for _, d := range []string{bin, fixtures, home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string][]byte{
		filepath.Join(bin, "curl"):                   []byte(fakeCurl),
		filepath.Join(fixtures, "SHA256SUMS"):        rel.sums,
		filepath.Join(fixtures, assetName+".tar.gz"): rel.archive,
	}
	for p, b := range files {
		if err := os.WriteFile(p, b, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("sh", filepath.Join("..", "..", "install.sh"))
	cmd.Env = append([]string{
		"PATH=" + bin + ":/usr/bin:/bin",
		"HOME=" + home,
		"FIXTURES=" + fixtures,
		"FAKE_LOG=" + filepath.Join(root, "curl.log"),
	}, env...)
	out, err := cmd.CombinedOutput()
	log, _ := os.ReadFile(filepath.Join(root, "curl.log"))
	return filepath.Join(home, ".local", "bin", "clifleet"), string(out) + "\n--- curl\n" + string(log), err
}

func TestInstallScriptInstallsTheLatestRelease(t *testing.T) {
	path, out, err := runInstall(t, newRelease(t, "clifleet "+tag))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	got, err := exec.Command(path, "version").Output()
	if err != nil || strings.TrimSpace(string(got)) != "clifleet "+tag || !strings.Contains(out, "add ") {
		t.Fatalf("%q %v\n%s", got, err, out)
	}
}

func TestInstallScriptPinsAVersion(t *testing.T) {
	path, out, err := runInstall(t, newRelease(t, "clifleet "+tag), "CLIFLEET_VERSION=9.9.9")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out, "/latest") {
		t.Fatalf("a pinned version still asked for the latest:\n%s", out)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestInstallScriptRefusesABadArchive(t *testing.T) {
	bad := newRelease(t, "clifleet "+tag)
	bad.sums = sumsFor([]byte("something else"), assetName)
	wrong := newRelease(t, "clifleet v1.0.0")
	for label, rel := range map[string]*release{"checksum": bad, "wrong build": wrong} {
		path, out, err := runInstall(t, rel)
		if err == nil {
			t.Fatalf("%s: installed anyway\n%s", label, out)
		}
		entries, _ := os.ReadDir(filepath.Dir(path))
		if len(entries) != 0 {
			t.Fatalf("%s: left %v\n%s", label, entries, out)
		}
	}
}
