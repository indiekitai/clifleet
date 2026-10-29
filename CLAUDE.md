# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

clifleet is a Go CLI that reports and upgrades AI coding CLIs (Claude Code, Codex, OpenCode, Cursor Agent, …) that are **already installed** on a fleet of hosts, over the system `ssh` (or local `sh`). Product rules live in `CONTRIBUTING.md`; live fleet / ops state for the owner's machines lives in `docs/HANDOFF.md`. Read both before changing upgrade behavior or touching a real fleet.

## Commands

Go 1.22+. CI (`.github/workflows/ci.yml`) runs exactly these:

```bash
go test ./...
go vet ./...
CGO_ENABLED=0 go build -trimpath -o clifleet ./cmd/clifleet
./clifleet version && ./clifleet --help   # CI smoke
```

Single package / single test:

```bash
go test ./internal/catalog -run TestCandidates -v
go test ./internal/probe -run 'TestFetch.*' -v
```

`internal/probe` tests pipe the generated scripts into a real `sh -s` with fake binaries, and apply scripts **move and overwrite whatever binary they find**. On 2026-10-29 a run on a Mac reached the real Homebrew prefix and overwrote the real `/opt/homebrew/lib/node_modules/@openai/codex/bin/codex.js`. Two guards now stand against that:

- `internal/probe/hermetic_test.go`: `TestMain` cuts `loginPathDirs` and `brewFallbacks` down to `$HOME`-relative entries (HOME is a temp dir in every test), and `TestScriptsStayOffTheRealPrefixes` fails if a test script still mentions `/opt/homebrew`, `/usr/local`, or `/home/linuxbrew`. A test that only inspects script text and needs the production list calls `withProductionPaths(t)`; it must never run that script.
- Locally on macOS, run the suite inside a sandbox that denies writes to real install locations (~1 min):

```bash
sandbox-exec -D HOME="$HOME" -p '(version 1)(allow default)(deny file-write* (subpath "/opt/homebrew") (subpath "/usr/local") (subpath (string-append (param "HOME") "/.local")) (subpath (string-append (param "HOME") "/.codex")) (subpath (string-append (param "HOME") "/.claude")) (subpath (string-append (param "HOME") "/.opencode")) (subpath (string-append (param "HOME") "/.cache/clifleet")))' go test -count=1 ./...
```

A new test that runs a script must pass its fake `bin` as `path_extra` and set `HOME` to a temp dir. `runSh` has a 1-minute deadline, so a fake that is being shadowed by a real binary shows up as a fast failure, not a hang.

`internal/cli.Version` defaults to `"0.1.0"`; release builds stamp it via `-ldflags -X github.com/indiekitai/clifleet/internal/cli.Version=<tag>` on `v*` tags (`release.yml`).

## Architecture

Data flow: `cmd/clifleet` → `cli` (flags, subcommands, table/JSON output, `last-report.json`) → `fleet` (per-host fan-out with a semaphore) → `probe` (builds POSIX sh scripts, parses their stdout) → `runner` (pipes the script into `sh -s` locally or `ssh … sh -s`). `catalog` is the pure decision layer; `config` loads `hosts.toml`.

- **Everything runs as a generated POSIX sh script on stdin.** There is no agent on the remote host. `probe` assembles a prelude + helper functions (`cf_cap` / `cf_limit` time caps in `probe.go`, the `engines.node` range check in `shell_safe.go`, brew `shellenv`, nvm) + per-tool blocks. Scripts emit tab-separated `CF1\t<tag>\t…` lines (`missing` / `present` / `upgrade` / `fetch` / `apply`) after a `clifleet-probe/1` marker; `Parse*` functions read only those lines. Changing a field means changing both the shell emitter and the Go parser.
- **Two passes per host.** First a read-only probe script finds which candidates exist and how they were installed (resolved real path, Homebrew Cellar/Caskroom token, npm package). Only then does `fleet` build a second script (upgrade, fetch, or apply) for tools the probe saw as present, and that script re-probes afterwards so rows show the new version.
- **`catalog` decides, `probe` executes.** `catalog.Candidates` merges global `tools`, host allowlist (`tools`), host blocklist (`skip`, wins) and `--only` (can only narrow). `catalog.Classify` / `Decide` map an `Observation` to an install method (`catalog.Method*`: native, npm, brew, bun, pnpm, yarn, cargo) and a one-shot command; `catalog.PlanStage` maps it to `stage` / `no-stage` / `skip` / `fail`. `Resolve` layers user `[[recipe]]` overlays onto built-in specs.
- **Upgrade phases** (`fleet.PhaseUpgrade` / `PhaseFetch` / `PhaseApply`): default `upgrade` = fetch then apply; `--fetch` / `--apply` run one half; `--one-shot` runs the in-place native updater. Staged artifacts go to `$XDG_CACHE_HOME/clifleet` (or `~/.cache/clifleet`) on each host. Staged kinds are npm (`npm pack` → temp-prefix install → directory swap with rollback), brew (`brew fetch` → `brew upgrade`), and release tarballs for native Codex / Claude Code / Cursor Agent (versioned dir + symlink flip, SHA check for Claude). Orchestration of the staged path is `fleet/stage.go`; scripts are `probe/stage.go`.
- **Timeouts are per host script and per tool.** `runner.Exec` puts the child in its own process group and SIGKILLs the group on deadline; a timeout message must say whether the remote script had started. `status` defaults to a shorter budget (`fleet.StatusBudget`) unless `timeout` is set explicitly.
- Exit codes: 0 all hosts done (missing tools count as success), 1 bad args / unreadable fleet file, 2 unreachable host or failed upgrade. One host failing never hides the others.

## Invariants (from CONTRIBUTING.md; tests guard these)

- **Never install.** A missing tool is a skip (`—`, `not installed`). No `brew install`, curl installers, or `npm install -g` of something not already on `PATH` — including under `--fetch`. A change that installs something absent is wrong.
- `--only` must not widen a host allowlist; `skip` beats `tools`.
- Built-in catalog has three tiers: default (`catalog.DefaultTools()` **and** the `tools` list in `config/example.go` — keep them in sync), opt-in (spec exists but not in `DefaultTools()`, e.g. `dsh`, `grok`, `kilo`, `kiro-cli`), and user `[[recipe]]`. Don't add regional or rename-prone tools to the default list. Leave a channel blank rather than guess an upgrade command (status still reports the binary; upgrade says `no upgrade recipe`).
- `cursor` (editor shim), `agent`, `cursor-agent` are three separate tools; don't merge them.
- Remote scripts must stay non-interactive: stdin closed for every child, `NONINTERACTIVE=1`, **do not set `CI=1`**, never source a login shell or profile. `--version` calls and nvm are time-capped and must not read the script's stdin.
- Native updaters without a stable artifact URL (`opencode upgrade`, `copilot update`, `pi update`, …) stay `no-stage`; don't add release-page scrapers. Native OpenCode is no-stage: staging it through npm would add a second install under `npm root -g`.
- A release is staged only when `obs.Real` is in the vendor's layout (`catalog.nativeRelease`: Codex `/standalone/`, Claude `/.local/share/claude/versions/`, Cursor Agent `/.local/share/cursor-agent/versions/`). Never key a replace on the tool name alone, because apply overwrites the file the probe found. `TestNativeReleaseNeedsVendorLayout` guards this. The apply scripts check the layout again (Codex under `standalone`, Claude/Cursor a symlink into `versions/`) and report `blocked` otherwise; there is no plain-file replace path.
- After a release swap, `cf_confirm_live` must see the staged version. `mismatch` and `broken` (exit 126/127 or a signal) roll back by retargeting links through `cf_relink_back`; a link that cannot be put back is reported as `rollback failed`, never "restored" (npm uses `cf_swap_undo` the same way). `broken` is checked before the version compare. Only keychain, timeout, and empty output count as soft success.
- Remote shell functions share global variables (`dest`, `prev`, `tool`, `root`; there is no `local`). A helper that assigns one of them clobbers its caller; the npm rollback in `cf_restore_labeled` broke this way. Give a helper's variables a prefix of its own (`cf_rl_*`, `cf_ar_*`), as the fixed helpers do.
- mise/asdf shims are never a swap target (`managed by mise`). Apply never unlocks the macOS keychain.

## Operating on the real fleet

`docs/HANDOFF.md` has the owner's hosts and preferences. In short: don't run a real `upgrade` / `--fetch` / `--apply` against the fleet without the owner confirming timing (`status`, `doctor`, and `--dry-run` are safe); prefer `--fetch` then `--apply` over `--one-shot`; never allowlist `dsh` on US hosts (`claw1`); after long runs read `~/.cache/clifleet/last-report.json`, and use `set -o pipefail` when piping through `tee`. Update `docs/HANDOFF.md` when `main` tip or fleet state changes.

This is the owner's own repo: commit (Chinese message) and push straight to `main`; no PRs.
