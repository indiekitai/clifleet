package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/indiekitai/clifleet/internal/selfupdate"
)

// The newer-release notice asks GitHub at most once a day (once an hour
// after a failure) and never makes a fleet run wait for it.
const (
	noticeFresh   = 24 * time.Hour
	noticeRetry   = time.Hour
	noticeTimeout = 6 * time.Second
)

type noticeCache struct {
	Checked time.Time `json:"checked"`
	Latest  string    `json:"latest,omitempty"`
}

// updateNotice returns a one-line hint when a newer clifleet release exists,
// else "". A build that is not a version (dev) gets no hint, and
// CLIFLEET_NO_UPDATE_CHECK=1 turns the check off.
func updateNotice(ctx context.Context, now time.Time, current string) string {
	if os.Getenv("CLIFLEET_NO_UPDATE_CHECK") != "" {
		return ""
	}
	if _, ok := selfupdate.Compare(current, current); !ok {
		return ""
	}
	path, err := cachePath("update-check.json")
	if err != nil {
		return ""
	}
	var c noticeCache
	if raw, err := os.ReadFile(path); err == nil && json.Unmarshal(raw, &c) == nil {
		age := now.Sub(c.Checked)
		fresh := age >= 0 && (age < noticeRetry || (c.Latest != "" && age < noticeFresh))
		if fresh {
			return noticeFor(current, c.Latest)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, noticeTimeout)
	defer cancel()
	latest, err := selfupdate.Latest(ctx, &http.Client{Timeout: noticeTimeout})
	if ctx.Err() == context.Canceled {
		return ""
	}
	if err != nil {
		latest = ""
	}
	if raw, err := json.Marshal(noticeCache{Checked: now, Latest: latest}); err == nil {
		if os.MkdirAll(filepath.Dir(path), 0o755) == nil {
			os.WriteFile(path, raw, 0o644)
		}
	}
	return noticeFor(current, latest)
}

func noticeFor(current, latest string) string {
	if strings.TrimSpace(latest) == "" {
		return ""
	}
	if cmp, ok := selfupdate.Compare(current, latest); !ok || cmp >= 0 {
		return ""
	}
	return fmt.Sprintf("clifleet %s is available (this is %s). Run clifleet update.", latest, current)
}

// startUpdateNotice runs updateNotice beside a fleet run. The returned
// func gives the hint if the check has already finished, else "".
func startUpdateNotice(ctx context.Context) func() string {
	ch := make(chan string, 1)
	go func() { ch <- updateNotice(ctx, time.Now(), version()) }()
	return func() string {
		select {
		case s := <-ch:
			return s
		default:
			return ""
		}
	}
}
