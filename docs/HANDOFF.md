# clifleet maintainer handoff

Last updated: 2026-10-29 (Asia/Tokyo). Previous maintainer: Indiekit (Grok Bot agent). Owner: Fan Yang.

## Purpose of this file

Operational state for whoever maintains the **public product** and Fan's **personal fleet**. Product docs stay in `README.md` / `CONTRIBUTING.md`. This file is the living progress + fleet note so a new agent can pick up without chat history.

## Repositories

| Role | URL |
| --- | --- |
| Public GitHub (source of truth for PRs/releases) | https://github.com/indiekitai/clifleet |
| Module path | `github.com/indiekitai/clifleet` |
| Private Origin (historical) | `fan-yang-7p3v0/clifleet` |

- License: MIT
- Language: Go 1.22+
- Default branch: `main`
- Topics on GitHub: `cli`, `ssh`, `fleet`, `claude-code`, `codex`, `opencode`, `golang` (done)
- CI: `.github/workflows/ci.yml`
- Release: `.github/workflows/release.yml` (Linux/macOS amd64+arm64). Tag builds stamp `-X github.com/indiekitai/clifleet/internal/cli.Version=${GITHUB_REF_NAME}`; an untagged `go install` reports the Go module version (pseudo-version), a plain source build `dev`. Before tagging, run `scripts/release-check.sh <new build>`: it runs `--help`, `status --json`, and `upgrade --dry-run --json` with the installed and the new binary on the real fleet, diffs them, and puts `last-report.json` back.

### Current tip (as of handoff)

Checked against `origin/main` on 2026-10-29 (Asia/Tokyo). Tip SHA, PR #2–#4 subjects, the `v0.1.0` tag, Go 1.22, topics, and the release ldflags line match this file. No open PRs. The only correction from the earlier draft: ldflags are already wired for **tag** builds, not still missing.

- Since 2026-10-29 evening `main` moves by **direct push** (no PRs; owner's call). Read the tip with `git log origin/main -1`. What landed that evening, after PR #5:
  - `08233f6` staged apply: vendor-layout gate, honest rollback reporting, Codex/Cursor link handling
  - `1bce61c` orchestration: ssh `--` and dash-destination rejection, duplicate-host rejection, doctor deadline, short-budget caps, partial-output salvage for apply / one-shot
  - `2d1be27` shell: whole-process-tree kill on caps, stalled-download cutoff, mawk-safe checksum, tab/newline paths, npm bin-link rollback
  - `4ea23b5` catalog: Volta and mise/asdf `installs/` are managed, whole-segment npm package match, custom `native` recipes are not staged
  - `3202b4e` + tag **`v0.2.0`**: `install.sh`, `clifleet update` (alias `self-update`), `SHA256SUMS` in releases, version from build info
  - `cb11ca0`: every recursive delete in remote scripts goes through `cf_rmtree`, which refuses empty, relative, `.`/`..`, `/`, `$HOME` and its parents (`TestShellDeletesTreesOnlyThroughRmtree` guards it)
  - `be8527c` + tag **`v0.2.1`**: release runs the tests first, Actions pinned to commit SHAs, build provenance attestation on the tarballs and `SHA256SUMS`; `install.sh` and `clifleet update` run `gh attestation verify` when gh is logged in (`CLIFLEET_NO_ATTESTATION=1` skips). Verified end to end 2026-10-29: release green, attestation verifies, install.sh and a real v0.2.0 → v0.2.1 update on Mac Studio
  - Tag **`v0.2.2`** (2026-10-30, release green, release-check showed only the intended help changes): `clifleet history` + `history.jsonl` (every non-dry-run upgrade / fetch / apply, one line per host × tool); `skills/clifleet/SKILL.md` + `TestSkillNamesOnlyRealCommandsAndFlags`; terminal-only "newer clifleet available" notice (`CLIFLEET_NO_UPDATE_CHECK=1` off); `scripts/release-check.sh`
  - Tag **`v0.2.3`** (2026-10-30, release-check showed only the intended LATEST / help changes): `76b136a` LATEST column in `status` and fetch skip for tools already on the newest version (`internal/latest`, `internal/semver`); then a failed fetch drops the tool's old stamp so apply cannot open a partial download
  - Tag **`v0.2.4`** (2026-10-30, after Fan's own trial of v0.2.3): output redone — `status` is a tool × host matrix (`--long` for the old list), `upgrade` / `--fetch` / `--apply` list only rows that changed or need attention and fold the rest into `Not shown`, dry-run says what would happen in words, per-tool progress lines and `✓ host (n/N)`, a next-step hint on a terminal; `9e1694c` fixes the four bugs the trial found (below) plus the npm 120 s cap and the duplicate agent / cursor-agent download; a brew apply whose reprobe moved the version counts as applied (`87cf88a`)
  - Tag **`v0.2.5`** (2026-10-30): table column widths ignore ANSI colour codes (the yellow `↑` cells had widened every status matrix column by ~9)
  - Tag **`v0.2.6`** (2026-10-30): scripts drop cmux's per-session wrapper dirs (`~/.cmuxterm/cmux-cli-shims/<id>/`) from PATH. Run from a cmux session, the local host showed amp and hermes as installed with `?` (only the wrappers exist on mac-studio); from a plain terminal they were correctly not installed
  - Before that: Ctrl-C is reported as `interrupted`, not `unreachable`; the kill-tree KILL trusts only PID+start time (BusyBox without `lstart` KILLs only a direct child that is still the watchdog's sibling); npm aliases keep their real package name; a staged run hit by Ctrl-C sets a host error, so it never exits 0
- `1cad8412` — squash-merge of **PR #4** (*Harden staged apply against mise shims, keychain rollbacks, and summary counts*)
- Prior merges worth knowing:
  - **PR #3** `b35f40c8` — staged upgrade is the default path (npm atomic temp-prefix apply, brew PATH / `shellenv`, native stage budget, engines preflight)
  - **PR #2** `77bbf8b1` — `--fetch` / `--apply`, SSH `-T`, nvm/`--version` caps, status default 45s
  - Catalog / hosts work landed earlier the same week

Open PRs: none at handoff time.

## Install on Fan's control machine (Mac Studio)

- Binary: `/Users/tf/go/bin/clifleet` (release build `v0.2.6`, updated 2026-10-30 with `clifleet update`; SHA256SUMS and attestation verified).
- The 2026-10-29 13:31 apply report is kept as `~/.cache/clifleet/last-report-20261029-1331.json`; the 2026-10-30 apply report as `last-report-20261030-apply.json`; `last-report.json` now holds the latest status run.
- Config: `~/.config/clifleet/hosts.toml` (timeout currently **`10m`**, concurrency `4`)
- Cache / durable last run: `~/.cache/clifleet/last-report.json` plus `npm/`, `release/`, `staged/`
- Control machine id for Cursor local tools: `4115756c-6089-4706-ab99-39b456da5650` (label Mac-Studio.local)
- Upgrade to the newest release: `clifleet update` (`--check` first if unsure). It only replaces clifleet itself; `upgrade` is the fleet command. Releases are cut by pushing a `v*` tag; `release.yml` publishes the four tarballs plus `SHA256SUMS`, which `update` and `install.sh` require.
- Fresh machine, no Go: `curl -fsSL https://raw.githubusercontent.com/indiekitai/clifleet/main/install.sh | sh` (add `CLIFLEET_INSTALL_DIR=$HOME/go/bin` to keep this machine's path).
- Untagged tip: `go install github.com/indiekitai/clifleet/cmd/clifleet@main` (reports a Go pseudo-version; `update` then moves it to the next release).

A working clone lives at `/Users/tf/Documents/Fansen/clifleet`.
- Agent skill: `skills/clifleet/SKILL.md` in the repo. Fan decided 2026-10-30 not to install it into `~/.claude/skills`; it stays a repo file for other users and for reference.
- Upgrade history: `~/.cache/clifleet/history.jsonl` starts with the next real upgrade; earlier runs are only in this file.

## Fleet (`hosts.toml`)

Four hosts. Global `tools` includes the default catalog **plus** opt-in `dsh`. **claw1 is US: do not allowlist `dsh` there.**

| Host | How reached | Notes |
| --- | --- | --- |
| `mac-studio` | `local = true` | Control laptop; Homebrew under `/opt/homebrew` |
| `mbp` | SSH alias `mbp` | User `tradingfront`; macOS login **keychain often locked** over SSH |
| `omarchy` | SSH alias `omarchy` | Linux; **codex is mise-managed** (`/usr/bin/mise` shim) |
| `claw1` | SSH alias `claw1` | Allowlist without `dsh`; openclaw may need newer Node (`engines` preflight blocks) |

SSH uses Fan's `~/.ssh/config` aliases. clifleet forces `BatchMode` + no TTY.

## Operator preferences (Fan)

These override generic “ask first” habits for **this** product:

1. **Confirm timing before any real upgrade** (`--fetch` / `--apply` / full `upgrade`) — upgrades can interrupt new CLI sessions.
2. **Routine catalog / PR / merge:** own review and merge; **do not** ping for approval on routine work. Escalate only if it changes product direction or could break fleets.
3. **Never finish silently.** Always send a short completion update when a batch ends (Fan hates having to ask 「还没好吗」).
4. **Download-then-atomic-swap** is the product expectation. Prefer staged `--fetch` then `--apply`. Avoid disruptive `--one-shot` / in-place updaters unless Fan asks.
5. Suggested ops pattern he already used: **fetch first**, report, then apply when he says so (e.g. 「全部 apply」). Do not invent a long unattended apply without timing OK.

## Newest-version check (main, after v0.2.2)

`status` has a LATEST column and `upgrade` / `--fetch` skip tools already on the newest version (looked up from the control machine: npm registry, formulae.brew.sh, the Codex / Claude / Cursor release endpoints). Known gaps: a Homebrew **tap** formula (mac-studio's opencode is `anomalyco/tap/opencode-v2`) is not in the formulae.brew.sh API, so LATEST stays empty and fetch still runs; native tools with no npm spec (opencode native on omarchy / claw1, aider, hermes, agy) show no LATEST.

Host facts found 2026-10-30: mbp's proxy is set by `proxy_on` in `~/.zshrc`, which clifleet's remote scripts do not read (by rule), so mbp downloads go direct; Fan could add a proxy to that host's environment if he wants. omarchy's `~/.local/bin/codex` is a hand-written wrapper over `mise x codex` (aqua backend); `mise upgrade codex` there moved it 0.162.1 → 0.165.0 on 2026-10-30.

## Last fleet run (2026-10-30, v0.2.2 binary, Fan OKed)

`upgrade --fetch` for all hosts, a `--fetch --hosts mbp --timeout 30m` retry, then `--apply`. The previous run's report is kept at `~/.cache/clifleet/last-report-20261029-1331.json` on the control machine.

- **mac-studio:** opencode 2.2.0 → **2.2.1** applied; the rest up to date. kimi / qwen / dsh show `update available` but are `no-stage` (only `--one-shot` moves them).
- **omarchy:** codex 0.162.1 → **0.165.0** by `mise upgrade codex` on the host (not by clifleet; the aqua backend already serves 0.165.0). opencode and pi are `update available` + `no-stage`.
- **mbp:**
  - claude still **2.1.296**: the 276 MB download timed out twice (191 MB in 30 min, direct link ~270 KB/s; the same file through mbp's proxy runs ~5 MB/s).
  - opencode: apply reported `blocked` (Xcode is too old), but brew had already poured and linked **2.2.1** (Cellar and link dated 01:45) and only then exited non-zero. A reporting bug, fixed on main after v0.2.3: a brew apply whose reprobe shows a new version is `applied`, with brew's complaint in the detail.
  - codex and agent apply failed with `could not extract`. Cause: a clifleet bug. A failed fetch left the 10-29 stamps pointing at files it had already deleted and partly re-downloaded. Fixed on main (`cf_unstage` at the start of every fetch, `TestFailedFetchDropsTheOldStamp`); nothing was swapped. The two stale stamps were gone after the v0.2.3 fetch (checked 2026-10-30).
  - agent / cursor-agent versions are unreadable (`keychain locked`).
- **claw1:** openclaw `blocked` (needs Node ≥ 24.22.1, has v22.23.4); the rest up to date.

## Fan's trial run (2026-10-30 afternoon, v0.2.3)

Fan ran `upgrade --fetch` then `--apply` himself. It surfaced four bugs, all fixed in v0.2.4 (`9e1694c`), nothing left broken on a host (every failed swap rolled back):

- Apply could downgrade from a stale stamp (mac-studio claude 2.1.314 → staged 2.1.312, the staged binary died with exit 137, rolled back). Now apply refuses a stamp older than the live version and drops it (`cf_refuse_older`; Cursor builds compare by date).
- npm Codex: the platform tarball's package.json is named `@openai/codex`, so the old swap wrote it over the main package and rolled back. Now the main package installs with `--omit=optional` and the platform package is unpacked into `@openai/codex/node_modules/@openai/codex-<platform>`, where `codex.js` looks for it (`cf_nest_platform`).
- claw1 claude sits behind a root-owned `/usr/local/bin/claude` → `~/.local/bin/claude` → `versions/`; apply tried to retarget the root link. Now it follows the chain to the link that points into `versions/` (`cf_versions_link`, same for Cursor Agent).
- Prereleases compared equal (dsh 0.2.1-rc.2 vs rc.5). Apply now compares the full version (`cf_semver_full`, `cf_ver_cmp`).

Stale stamps from that run were deleted by hand (Fan OKed): mac-studio `claude`, `agent`, `cursor-agent`; mbp `opencode`, `pi`; claw1 `agent`, `cursor-agent`, `opencode`. Fresh stamps left for the next `--apply`: mac-studio `dsh`, `opencode`; mbp `claude`, `cursor-agent`, `dsh`; claw1 `claude` (2.1.314, the root-link case), `codex`, `openclaw`. Not yet applied with v0.2.4 (needs Fan's timing OK): claw1 claude 2.1.312 → 2.1.314, codex 0.165.0 → 0.166.0 (npm, mac-studio / claw1; mbp is native), dsh rc.2 → rc.5. Cause of the exit 137 itself (staged claude 2.1.312 killed on mac-studio) is unknown.

## Open gaps (product vs host)

| Issue | Whose problem | Impact on Fan | Likely next step |
| --- | --- | --- | --- |
| Host `timeout` 10m still cuts large Claude/Cursor CDN downloads on slow links | Product + config | Stale CLIs on mbp/omarchy; long runs exit 2 | Raise timeout for targeted refetch **or** per-tool budget / resume; ask Fan before another 40m+ run |
| mbp Xcode / CLT vs Homebrew needing newer Xcode (seen as brew blocked) | Host | brew-channel tools (e.g. opencode) stuck | Fan upgrades Xcode on mbp |
| mbp login keychain locked over SSH | Host | agent/`cursor-agent` version probe / some applies painful | Unlock keychain on mbp (or run apply from an unlocked GUI session) |
| omarchy codex behind mise | Host / policy | clifleet correctly **refuses** to replace the mise binary | `mise upgrade codex` on the host (done 2026-10-30, 0.165.0) |
| mbp downloads go direct (proxy only in `~/.zshrc`) | Host | Big release downloads (claude 276 MB) time out | Not a clifleet problem (Fan, 2026-10-30): no per-host proxy / env option; a slow link is the host owner's to fix |
| claw1 openclaw Node engines too old | Host | fetch `blocked (needs Node …)` | Upgrade Node on claw1; clifleet will not bump Node |

## Safe command cheat sheet

```bash
clifleet doctor
clifleet status --timeout 45s
clifleet status --hosts omarchy --only codex

# Preferred upgrade path
clifleet upgrade --fetch --only claude,codex,opencode,agent,cursor-agent
# …report to Fan, get timing OK…
clifleet upgrade --apply --only claude,codex,opencode,agent,cursor-agent

# Narrow retries
clifleet upgrade --fetch --hosts mbp,omarchy --only claude,opencode,agent --timeout 20m
```

Always `set -o pipefail` if piping through `tee`. Read `last-report.json` after long runs instead of trusting a scrolled terminal alone.

## What not to do

- Do not install missing CLIs (`brew install` / curl installers) — product rule; see `CONTRIBUTING.md`.
- Do not put `dsh` on claw1 or other US hosts.
- Do not treat a mise/asdf shim as an atomic replace target.
- Do not unlock or scrape macOS keychain secrets from automation.
- Do not start a long fleet fetch/apply without Fan's timing confirmation.
- Do not silently end a maintenance batch.

## Backlog

From comparing with Mole (`docs/reviews/2026-10-29-参考-Mole.md`, local only). Done 2026-10-29: rm guard (`cf_rmtree`), release hardening (v0.2.1), agent skill + help test, upgrade history, newer-release notice, `scripts/release-check.sh`.

Left, none started (from the code review, `docs/reviews/2026-10-29-代码审查.md`, local only):

1. Move the remote shell out of Go strings into `go:embed` `.sh` files, then run `shellcheck -s sh` / `dash -n` / `checkbashisms` in CI.
2. Remote lock per host: stops two concurrent applies on one machine and gives a Ctrl-C'd run's remote step a way to stop.
3. Done in v0.2.4: progress lines, status matrix, next-step hint, readable dry-run, apply lists only what changed, npm fetch limit, shared agent / cursor-agent download, and the four trial bugs (see "Fan's trial run"). Left from the same trial:
   - An `--apply` dry run only probes, so it cannot see which tools have a stamp; its rows say `would apply if staged`. Proper fix: the probe emits a `staged` line per tool (stamp version), the parser and `renderStageRow` use it, and dry-run shows `would apply <old> → <new>` or folds the row.
   - dry-run promises to stage claw1 openclaw, but the Node engines check only runs at fetch, so the real run says `blocked`; dry-run should run the same preflight or say "may be blocked".
   - mac-studio opencode (`anomalyco/tap/opencode-v2`, not in the formulae.brew.sh API, so no LATEST) runs `brew update && brew fetch` every time even when current.
9. Smaller: `hostRun` struct for the shared parameters of `finishStage` / `finishSafe` / `runStageSteps` / `oneHost`; an AGENTS.md pointing Codex at CLAUDE.md.

## Suggested first week for the next maintainer

1. Read `README.md` (staged upgrades + timeout semantics) and this file.
2. `clifleet doctor` + `clifleet status` from Mac Studio; confirm SSH to mbp/omarchy/claw1.
3. Decide with Fan: **longer timeout refetch** vs **host-side Xcode / keychain / mise / Node**.
4. Only after timing OK, refetch the lagging hosts/tools, then apply.
5. New releases: push a `v*` tag; `clifleet update` on each machine picks it up.

## Contact / ownership note

Fan asked Indiekit (2026-10-29) to refresh docs/progress and hand maintenance to **another agent**. Update this file when fleet state or `main` tip changes.
