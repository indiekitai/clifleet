// Package latest asks the vendor, from the control machine, which version of
// a tool is newest on the channel it was installed from. It never runs on a
// host and never downloads an artifact.
package latest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Kind is the channel a version comes from.
type Kind string

const (
	NPM         Kind = "npm"
	BrewFormula Kind = "brew-formula"
	BrewCask    Kind = "brew-cask"
	Codex       Kind = "release-codex"
	Claude      Kind = "release-claude"
	Cursor      Kind = "release-cursor"
)

// Query names one channel and, for npm and Homebrew, the package or token.
type Query struct {
	Kind Kind
	Key  string
}

// Endpoints. Tests point them at a local server. The release URLs are the
// ones the fetch scripts read, so status and fetch agree on "latest".
var (
	NpmRegistry   = "https://registry.npmjs.org"
	BrewAPI       = "https://formulae.brew.sh/api"
	CodexChannel  = "https://releases.openai.com/codex/channels/latest"
	ClaudeLatest  = "https://downloads.claude.ai/claude-code-releases/latest"
	CursorInstall = "https://cursor.com/install"
)

const maxBody = 4 << 20

var (
	npmName   = regexp.MustCompile(`^(@[A-Za-z0-9][A-Za-z0-9._-]*/)?[A-Za-z0-9][A-Za-z0-9._-]*$`)
	brewToken = regexp.MustCompile(`^[a-z0-9][a-z0-9@+._-]*$`)
	versionOK = regexp.MustCompile(`^[0-9][0-9A-Za-z.+-]{0,63}$`)
	cursorVer = regexp.MustCompile(`downloads\.cursor\.com/lab/([0-9]{4}\.[0-9]{2}\.[0-9]{2}-[A-Za-z0-9]+)/`)
)

// Lookup returns the newest version on q's channel.
func Lookup(ctx context.Context, client *http.Client, q Query) (string, error) {
	var v string
	var err error
	switch q.Kind {
	case NPM:
		if !npmName.MatchString(q.Key) {
			return "", fmt.Errorf("npm package %q", q.Key)
		}
		var doc struct {
			Version string `json:"version"`
		}
		err = getJSON(ctx, client, NpmRegistry+"/"+strings.Replace(q.Key, "/", "%2F", 1)+"/latest", &doc)
		v = doc.Version
	case BrewFormula:
		// A tap formula (user/tap/name) is not in the formulae.brew.sh API.
		if !brewToken.MatchString(q.Key) {
			return "", fmt.Errorf("brew formula %q", q.Key)
		}
		var doc struct {
			Versions struct {
				Stable string `json:"stable"`
			} `json:"versions"`
		}
		err = getJSON(ctx, client, BrewAPI+"/formula/"+q.Key+".json", &doc)
		v = doc.Versions.Stable
	case BrewCask:
		if !brewToken.MatchString(q.Key) {
			return "", fmt.Errorf("brew cask %q", q.Key)
		}
		var doc struct {
			Version string `json:"version"`
		}
		err = getJSON(ctx, client, BrewAPI+"/cask/"+q.Key+".json", &doc)
		// Casks append a build after a comma: 1.2.3,4567.
		v, _, _ = strings.Cut(doc.Version, ",")
	case Codex:
		var doc struct {
			Tag string `json:"tag_name"`
		}
		err = getJSON(ctx, client, CodexChannel, &doc)
		v = strings.TrimPrefix(doc.Tag, "rust-v")
	case Claude:
		var body []byte
		body, err = get(ctx, client, ClaudeLatest)
		v = strings.TrimSpace(string(body))
	case Cursor:
		var body []byte
		body, err = get(ctx, client, CursorInstall)
		if m := cursorVer.FindSubmatch(body); m != nil {
			v = string(m[1])
		}
	default:
		return "", fmt.Errorf("no latest-version source for %q", q.Kind)
	}
	if err != nil {
		return "", err
	}
	if !versionOK.MatchString(v) {
		return "", fmt.Errorf("%s %s: no version in the answer", q.Kind, q.Key)
	}
	return v, nil
}

func get(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxBody))
}

func getJSON(ctx context.Context, client *http.Client, url string, into any) error {
	body, err := get(ctx, client, url)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, into)
}

// Resolver memoizes lookups for one run, so four hosts with the same tool
// cost one request. A failed lookup is "" and is not retried in that run.
type Resolver struct {
	client  *http.Client
	timeout time.Duration
	mu      sync.Mutex
	seen    map[Query]*entry
}

type entry struct {
	once sync.Once
	v    string
}

// NewResolver gives each lookup timeout. The client honors the usual proxy
// environment variables.
func NewResolver(timeout time.Duration) *Resolver {
	return &Resolver{client: &http.Client{Timeout: timeout}, timeout: timeout, seen: map[Query]*entry{}}
}

// Latest is the newest version for q, or "" when it could not be found.
func (r *Resolver) Latest(ctx context.Context, q Query) string {
	r.mu.Lock()
	e, ok := r.seen[q]
	if !ok {
		e = &entry{}
		r.seen[q] = e
	}
	r.mu.Unlock()
	e.once.Do(func() {
		ctx, cancel := context.WithTimeout(ctx, r.timeout)
		defer cancel()
		if v, err := Lookup(ctx, r.client, q); err == nil {
			e.v = v
		}
	})
	return e.v
}
