package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/indiekitai/clifleet/internal/selfupdate"
)

func TestUpdateNoticeCachesTheCheck(t *testing.T) {
	var hits atomic.Int32
	var down atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		hits.Add(1)
		if down.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		http.Redirect(w, req, "/releases/tag/v0.3.0", http.StatusFound)
	}))
	defer srv.Close()
	oldURL := selfupdate.BaseURL
	defer func() { selfupdate.BaseURL = oldURL }()
	selfupdate.BaseURL = srv.URL + "/releases"
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("CLIFLEET_NO_UPDATE_CHECK", "")
	ctx := context.Background()
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

	got := updateNotice(ctx, now, "v0.2.1")
	if !strings.Contains(got, "clifleet v0.3.0 is available (this is v0.2.1)") || hits.Load() != 1 {
		t.Fatalf("%q hits=%d", got, hits.Load())
	}
	// Within a day the cached answer is used, also for a version that is current.
	if got := updateNotice(ctx, now.Add(23*time.Hour), "v0.2.1"); got == "" || hits.Load() != 1 {
		t.Fatalf("cached %q hits=%d", got, hits.Load())
	}
	if got := updateNotice(ctx, now.Add(time.Hour), "v0.3.0"); got != "" {
		t.Fatalf("current build got %q", got)
	}
	// After a day it asks again; a failure is cached for an hour only.
	down.Store(true)
	if got := updateNotice(ctx, now.Add(25*time.Hour), "v0.2.1"); got != "" || hits.Load() != 2 {
		t.Fatalf("failed check %q hits=%d", got, hits.Load())
	}
	updateNotice(ctx, now.Add(25*time.Hour+30*time.Minute), "v0.2.1")
	if hits.Load() != 2 {
		t.Fatalf("failure not cached, hits=%d", hits.Load())
	}
	down.Store(false)
	if got := updateNotice(ctx, now.Add(26*time.Hour+time.Minute), "v0.2.1"); got == "" || hits.Load() != 3 {
		t.Fatalf("retry %q hits=%d", got, hits.Load())
	}

	for _, cur := range []string{"dev", ""} {
		if got := updateNotice(ctx, now.Add(48*time.Hour), cur); got != "" {
			t.Fatalf("%q build got %q", cur, got)
		}
	}
	t.Setenv("CLIFLEET_NO_UPDATE_CHECK", "1")
	if got := updateNotice(ctx, now.Add(72*time.Hour), "v0.2.1"); got != "" || hits.Load() != 3 {
		t.Fatalf("disabled %q hits=%d", got, hits.Load())
	}
}
