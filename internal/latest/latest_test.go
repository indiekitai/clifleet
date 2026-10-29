package latest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestLookupEachChannel(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		hits.Add(1)
		switch req.URL.EscapedPath() {
		case "/npm/@openai%2Fcodex/latest":
			w.Write([]byte(`{"name":"@openai/codex","version":"0.165.0"}`))
		case "/npm/aider/latest":
			w.Write([]byte(`{"version":"<script>"}`))
		case "/brew/formula/opencode.json":
			w.Write([]byte(`{"versions":{"stable":"2.2.1","head":"HEAD"}}`))
		case "/brew/cask/codex.json":
			w.Write([]byte(`{"version":"0.165.0,1234"}`))
		case "/codex":
			w.Write([]byte(`{"assets":[],"tag_name":"rust-v0.165.0"}`))
		case "/claude":
			w.Write([]byte("2.1.312\n"))
		case "/cursor":
			w.Write([]byte(`DOWNLOAD_URL="https://downloads.cursor.com/lab/2026.10.22-27dcf5f/${OS}/${ARCH}/agent-cli-package.tar.gz"`))
		default:
			http.NotFound(w, req)
		}
	}))
	defer srv.Close()
	defer swap(&NpmRegistry, srv.URL+"/npm")()
	defer swap(&BrewAPI, srv.URL+"/brew")()
	defer swap(&CodexChannel, srv.URL+"/codex")()
	defer swap(&ClaudeLatest, srv.URL+"/claude")()
	defer swap(&CursorInstall, srv.URL+"/cursor")()

	ctx := context.Background()
	cases := []struct {
		q    Query
		want string
		ok   bool
	}{
		{Query{NPM, "@openai/codex"}, "0.165.0", true},
		{Query{BrewFormula, "opencode"}, "2.2.1", true},
		{Query{BrewCask, "codex"}, "0.165.0", true},
		{Query{Codex, ""}, "0.165.0", true},
		{Query{Claude, ""}, "2.1.312", true},
		{Query{Cursor, ""}, "2026.10.22-27dcf5f", true},
		{Query{NPM, "aider"}, "", false},
		{Query{NPM, "missing"}, "", false},
		{Query{NPM, "../../etc"}, "", false},
		{Query{BrewFormula, "user/tap/thing"}, "", false},
		{Query{"pip", "aider"}, "", false},
	}
	for _, c := range cases {
		got, err := Lookup(ctx, srv.Client(), c.q)
		if got != c.want || (err == nil) != c.ok {
			t.Errorf("Lookup(%+v) = %q, %v; want %q ok=%v", c.q, got, err, c.want, c.ok)
		}
	}
}

func TestResolverAsksOncePerRun(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		hits.Add(1)
		if req.URL.Path == "/slow" {
			time.Sleep(2 * time.Second)
		}
		w.Write([]byte("2.1.312"))
	}))
	defer srv.Close()
	defer swap(&ClaudeLatest, srv.URL+"/claude")()

	r := NewResolver(time.Second)
	done := make(chan string, 4)
	for i := 0; i < 4; i++ {
		go func() { done <- r.Latest(context.Background(), Query{Kind: Claude}) }()
	}
	for i := 0; i < 4; i++ {
		if v := <-done; v != "2.1.312" {
			t.Fatalf("got %q", v)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("%d requests for one query", hits.Load())
	}

	ClaudeLatest = srv.URL + "/slow"
	r = NewResolver(200 * time.Millisecond)
	if v := r.Latest(context.Background(), Query{Kind: Claude}); v != "" {
		t.Fatalf("timed-out lookup gave %q", v)
	}
}

func swap(p *string, v string) func() {
	old := *p
	*p = v
	return func() { *p = old }
}
