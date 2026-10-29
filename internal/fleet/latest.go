package fleet

import (
	"context"
	"sync"

	"github.com/indiekitai/clifleet/internal/catalog"
	"github.com/indiekitai/clifleet/internal/latest"
	"github.com/indiekitai/clifleet/internal/probe"
	"github.com/indiekitai/clifleet/internal/semver"
)

// latestQuery picks the channel to ask about p. fetchable is true when it is
// the channel a fetch would download from, so an equal version can skip that
// download. A no-stage tool with an npm spec is only shown (same version
// line), never skipped.
func latestQuery(p plan, override string) (q latest.Query, fetchable, ok bool) {
	st := p.stage
	if st.Action == "" {
		st = catalog.PlanStage(p.spec, p.obs, override)
	}
	if st.Action == catalog.ActionStage {
		switch st.Kind {
		case catalog.MethodNpm:
			return latest.Query{Kind: latest.NPM, Key: st.Package}, true, true
		case catalog.MethodBrew:
			if st.BrewKind == "cask" {
				return latest.Query{Kind: latest.BrewCask, Key: st.BrewToken}, true, true
			}
			return latest.Query{Kind: latest.BrewFormula, Key: st.BrewToken}, true, true
		case catalog.KindRelease:
			switch st.Release {
			case "codex":
				return latest.Query{Kind: latest.Codex}, true, true
			case "claude":
				return latest.Query{Kind: latest.Claude}, true, true
			case "agent", "cursor_agent":
				return latest.Query{Kind: latest.Cursor}, true, true
			}
		}
	}
	if p.spec.Npm != "" {
		return latest.Query{Kind: latest.NPM, Key: p.spec.Npm}, false, true
	}
	return latest.Query{}, false, false
}

// markLatest asks for the newest version of every installed tool, all at
// once. When skipFetch is set, a tool whose installed version already equals
// the newest one on its fetch channel is marked current and gets no fetch.
func markLatest(ctx context.Context, opt Options, plans []plan, overrides map[string]string, skipFetch bool) {
	if opt.Latest == nil {
		return
	}
	var wg sync.WaitGroup
	for i := range plans {
		p := &plans[i]
		if !p.obs.Present {
			continue
		}
		q, fetchable, ok := latestQuery(*p, overrides[p.spec.Name])
		if !ok {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.latest = opt.Latest(ctx, q)
			if skipFetch && fetchable && !unreadableVersion(p.obs.Version) &&
				latestStatus(probe.ShortVersion(p.obs.Version), p.latest) == StCurrent {
				p.current = true
			}
		}()
	}
	wg.Wait()
}

// latestStatus compares an installed version with the newest one: up to
// date, update available, or "" when it cannot tell (unknown version, a
// build ahead of the channel, or versions that do not order).
func latestStatus(installed, newest string) string {
	if installed == "" || installed == "unknown" || newest == "" {
		return ""
	}
	if installed == newest {
		return StCurrent
	}
	c, ok := semver.Compare(installed, newest)
	switch {
	case !ok:
		return ""
	case c < 0:
		return StBehind
	case c == 0:
		return StCurrent
	}
	return ""
}

// withLatest copies each tool's newest version into its row. For status, an
// installed row also says whether it is up to date.
func withLatest(result HostResult, plans []plan, status bool) HostResult {
	newest := map[string]string{}
	for _, p := range plans {
		if p.latest != "" {
			newest[p.spec.Name] = p.latest
		}
	}
	for i := range result.Rows {
		row := &result.Rows[i]
		v, ok := newest[row.Tool]
		if !ok {
			continue
		}
		row.Latest = v
		if status && row.Status == StInstalled {
			if s := latestStatus(row.Version, v); s != "" {
				row.Status = s
			}
		}
	}
	return result
}
