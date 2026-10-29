// Package selfupdate replaces the running clifleet with a GitHub release
// build. It only ever touches clifleet's own binary, never a host tool.
package selfupdate

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/indiekitai/clifleet/internal/semver"
)

// BaseURL is the releases page. Tests point it at a local server.
var BaseURL = "https://github.com/indiekitai/clifleet/releases"

// GH is the gh binary that checks build attestations. Tests replace it.
var GH = "gh"

const (
	repo           = "indiekitai/clifleet"
	signerWorkflow = "indiekitai/clifleet/.github/workflows/release.yml"
)

const maxDownload = 64 << 20

// Options says what to replace and how.
type Options struct {
	Current string // version of the running binary; not semver for a source build
	Target  string // the file to replace
	GOOS    string
	GOARCH  string
	Check   bool // report only
	Force   bool // reinstall the same version, or replace a source build
	// NoAttestation skips gh attestation verify; SHA256SUMS is still checked.
	NoAttestation bool
	Client        *http.Client
}

// Result is what Update found and did.
type Result struct {
	Current   string
	Latest    string
	Available bool // Latest is newer, or Current is not a release version
	Updated   bool
	Attested  bool // gh confirmed the release workflow built the archive
}

// Update installs the latest release over o.Target when it is newer.
// Nothing is replaced unless the archive matches SHA256SUMS, has a valid
// build attestation when a logged-in gh is available, and the new binary
// runs and reports the release tag.
func Update(ctx context.Context, o Options) (Result, error) {
	res := Result{Current: o.Current}
	if !supported(o.GOOS, o.GOARCH) {
		return res, fmt.Errorf("no release build for %s/%s; build from source with go install", o.GOOS, o.GOARCH)
	}
	client := o.Client
	if client == nil {
		client = http.DefaultClient
	}
	latest, err := Latest(ctx, client)
	if err != nil {
		return res, err
	}
	res.Latest = latest
	cmp, ok := semver.Compare(o.Current, latest)
	res.Available = !ok || cmp < 0
	if o.Check {
		return res, nil
	}
	if !ok && !o.Force {
		return res, fmt.Errorf("clifleet %s was not built from a release; reinstall it with go install, or pass --force to replace it with %s", o.Current, latest)
	}
	if ok && cmp >= 0 && !o.Force {
		return res, nil
	}
	attested, err := install(ctx, client, latest, o)
	if err != nil {
		return res, err
	}
	res.Updated = true
	res.Attested = attested
	return res, nil
}

func supported(goos, goarch string) bool {
	return (goos == "darwin" || goos == "linux") && (goarch == "amd64" || goarch == "arm64")
}

// Latest reads the newest release tag from the /latest redirect. The
// GitHub API would need JSON and has a low anonymous rate limit.
func Latest(ctx context.Context, client *http.Client) (string, error) {
	noFollow := *client
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, BaseURL+"/latest", nil)
	if err != nil {
		return "", err
	}
	resp, err := noFollow.Do(req)
	if err != nil {
		return "", fmt.Errorf("find the latest release: %w", err)
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode < 300 || resp.StatusCode > 399 || !strings.Contains(loc, "/tag/") {
		return "", fmt.Errorf("find the latest release: %s answered %s", BaseURL+"/latest", resp.Status)
	}
	tag := path.Base(loc)
	if _, ok := semver.Compare(tag, tag); !ok || !strings.HasPrefix(tag, "v") {
		return "", fmt.Errorf("find the latest release: unexpected tag %q", tag)
	}
	return tag, nil
}

func install(ctx context.Context, client *http.Client, tag string, o Options) (bool, error) {
	name := fmt.Sprintf("clifleet_%s_%s", o.GOOS, o.GOARCH)
	base := BaseURL + "/download/" + tag + "/"
	sums, err := download(ctx, client, base+"SHA256SUMS")
	if err != nil {
		return false, err
	}
	want, err := sumFor(sums, name+".tar.gz")
	if err != nil {
		return false, fmt.Errorf("%s: %w", tag, err)
	}
	archive, err := download(ctx, client, base+name+".tar.gz")
	if err != nil {
		return false, err
	}
	got := sha256.Sum256(archive)
	if hex.EncodeToString(got[:]) != want {
		return false, fmt.Errorf("%s.tar.gz does not match SHA256SUMS of %s; nothing was replaced", name, tag)
	}
	attested, err := attest(ctx, archive, name, tag, o.NoAttestation)
	if err != nil {
		return false, err
	}
	bin, err := extract(archive, name)
	if err != nil {
		return false, err
	}

	tmp, err := os.CreateTemp(filepath.Dir(o.Target), ".clifleet-update-*")
	if err != nil {
		return false, fmt.Errorf("cannot write next to %s: %w", o.Target, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	_, werr := tmp.Write(bin)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		return false, fmt.Errorf("write %s: %w", tmpName, errors.Join(werr, cerr))
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return false, err
	}
	// A binary for the wrong platform, or a broken one, must not replace a
	// working clifleet.
	vctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(vctx, tmpName, "version").Output()
	if err != nil || strings.TrimSpace(string(out)) != "clifleet "+tag {
		return false, fmt.Errorf("the %s build does not run here (%q, %v); nothing was replaced", tag, strings.TrimSpace(string(out)), err)
	}
	if err := os.Rename(tmpName, o.Target); err != nil {
		return false, fmt.Errorf("replace %s: %w", o.Target, err)
	}
	return attested, nil
}

// attest runs gh attestation verify when gh is installed and logged in.
// SHA256SUMS ships with the archive, so it only proves the download is
// intact; the attestation proves this repo's release workflow built it.
// Without gh there is nothing to check against, and that is not an error.
func attest(ctx context.Context, archive []byte, name, tag string, skip bool) (bool, error) {
	if skip {
		return false, nil
	}
	gh, err := exec.LookPath(GH)
	if err != nil {
		return false, nil
	}
	actx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if exec.CommandContext(actx, gh, "auth", "status").Run() != nil {
		return false, nil
	}
	dir, err := os.MkdirTemp("", "clifleet-attest-*")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, name+".tar.gz")
	if err := os.WriteFile(file, archive, 0o644); err != nil {
		return false, err
	}
	out, err := exec.CommandContext(actx, gh, "attestation", "verify", file, "--repo", repo, "--signer-workflow", signerWorkflow).CombinedOutput()
	if err != nil {
		line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
		return false, fmt.Errorf("%s.tar.gz of %s has no valid build attestation from %s (%s); nothing was replaced (CLIFLEET_NO_ATTESTATION=1 relies on SHA256SUMS alone)", name, tag, repo, line)
	}
	return true, nil
}

func download(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload+1))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	if len(data) > maxDownload {
		return nil, fmt.Errorf("download %s: larger than %d bytes", url, maxDownload)
	}
	return data, nil
}

// sumFor reads sha256sum output ("<hex>  <file>", or "*<file>" in binary mode).
func sumFor(sums []byte, file string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == file && len(fields[0]) == 64 {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("%s is not listed in SHA256SUMS", file)
}

func extract(archive []byte, name string) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("read %s.tar.gz: %w", name, err)
	}
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("%s is not in %s.tar.gz", name, name)
		}
		if err != nil {
			return nil, fmt.Errorf("read %s.tar.gz: %w", name, err)
		}
		if hdr.Typeflag != tar.TypeReg || path.Base(hdr.Name) != name {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(tr, maxDownload+1))
		if err != nil {
			return nil, fmt.Errorf("read %s.tar.gz: %w", name, err)
		}
		if len(data) > maxDownload {
			return nil, fmt.Errorf("%s in %s.tar.gz is larger than %d bytes", name, name, maxDownload)
		}
		return data, nil
	}
}
