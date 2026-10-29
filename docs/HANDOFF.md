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
- Release: `.github/workflows/release.yml` (Linux/macOS amd64+arm64). Tag `v0.1.0` was part of the original handoff. `go install` of a later squash commit still prints `0.1.0` because `internal/cli.Version` defaults to that string. Tag builds already pass `-X github.com/indiekitai/clifleet/internal/cli.Version=${GITHUB_REF_NAME}`; an untagged install does not.

### Current tip (as of handoff)

Checked against `origin/main` on 2026-10-29 (Asia/Tokyo). Tip SHA, PR #2–#4 subjects, the `v0.1.0` tag, Go 1.22, topics, and the release ldflags line match this file. No open PRs. The only correction from the earlier draft: ldflags are already wired for **tag** builds, not still missing.

- Since 2026-10-29 evening `main` moves by **direct push** (no PRs; owner's call). `08233f6` fixed five staged-apply must-fix issues (vendor-layout gate, rollback reporting, Codex/Cursor link handling); the next push adds ssh `--` / dash-destination rejection, duplicate-host rejection, a doctor deadline, short-budget caps, and partial-output salvage for apply / one-shot. Read the tip with `git log origin/main -1`.
- `1cad8412` — squash-merge of **PR #4** (*Harden staged apply against mise shims, keychain rollbacks, and summary counts*)
- Prior merges worth knowing:
  - **PR #3** `b35f40c8` — staged upgrade is the default path (npm atomic temp-prefix apply, brew PATH / `shellenv`, native stage budget, engines preflight)
  - **PR #2** `77bbf8b1` — `--fetch` / `--apply`, SSH `-T`, nvm/`--version` caps, status default 45s
  - Catalog / hosts work landed earlier the same week

Open PRs: none at handoff time.

## Install on Fan's control machine (Mac Studio)

- Binary: `/Users/tf/go/bin/clifleet` (reports `clifleet 0.1.0`; built from `1cad8412` on 2026-10-29 ~11:23 JST)
- Config: `~/.config/clifleet/hosts.toml` (timeout currently **`10m`**, concurrency `4`)
- Cache / durable last run: `~/.cache/clifleet/last-report.json` plus `npm/`, `release/`, `staged/`
- Control machine id for Cursor local tools: `4115756c-6089-4706-ab99-39b456da5650` (label Mac-Studio.local)
- Reinstall from tip:

```bash
go install github.com/indiekitai/clifleet/cmd/clifleet@1cad8412
# or @latest after the next tag
```

The binary above is older than `main`; reinstall with `go install github.com/indiekitai/clifleet/cmd/clifleet@main` before the next real run. A working clone now lives at `/Users/tf/Documents/Fansen/clifleet`.

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

## Last fleet run (2026-10-29 ~13:31 JST)

Command shape: after PR #4 install,

```bash
set -o pipefail
clifleet upgrade --fetch --only claude,codex,opencode,agent,cursor-agent
clifleet upgrade --apply --only claude,codex,opencode,agent,cursor-agent
```

Rough outcome (see also `~/.cache/clifleet/last-report.json` on the control machine; that file is private and is not in this repo):

- **Fetch:** ~8 staged / ~12 failed — many `timed out` on mac-studio / mbp / omarchy CDN paths; claw1 staged cleanly for the five-pack.
- **Apply:** **0 applied** · 14 up to date · **3 failed** · 3 skipped (`nothing staged`)
  - mac-studio + claw1 five-pack: already current
  - **mbp:** claude / opencode / cursor-agent never staged (timeout / brew); **agent** failed extract and shows `keychain locked`
  - **omarchy:** **codex** extract failed and `version_raw` shows mise; **opencode** npm apply failed (versions observed around 2.x)

PR #4 diagnostics that did help: `keychain locked` surfaces; mise shows in `version_raw` / `managed by mise`; failed footer can note host count; longer release fetch budget; brew update failure no longer blocks `brew fetch`.

## Open gaps (product vs host)

| Issue | Whose problem | Impact on Fan | Likely next step |
| --- | --- | --- | --- |
| Host `timeout` 10m still cuts large Claude/Cursor CDN downloads on slow links | Product + config | Stale CLIs on mbp/omarchy; long runs exit 2 | Raise timeout for targeted refetch **or** per-tool budget / resume; ask Fan before another 40m+ run |
| mbp Xcode / CLT vs Homebrew needing newer Xcode (seen as brew blocked) | Host | brew-channel tools (e.g. opencode) stuck | Fan upgrades Xcode on mbp |
| mbp login keychain locked over SSH | Host | agent/`cursor-agent` version probe / some applies painful | Unlock keychain on mbp (or run apply from an unlocked GUI session) |
| omarchy codex behind mise | Host / policy | clifleet correctly **refuses** to replace the mise binary | `mise upgrade codex` (or install a real standalone and stop using the shim) |
| claw1 openclaw Node engines too old | Host | fetch `blocked (needs Node …)` | Upgrade Node on claw1; clifleet will not bump Node |
| `go install` of an untagged commit still prints `0.1.0` | Product | Confusing “am I on tip?” | Release builds already stamp `${GITHUB_REF_NAME}`. Cut `v0.1.1` or `v0.2.0` for those binaries, and bump `internal/cli.Version` if `go install @<sha>` should match tip |
| No local Fansen checkout | Ops | Agents may look in the wrong place | Prefer GitHub + `go install`; optional clone only if Fan asks |

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

## Suggested first week for the next maintainer

1. Read `README.md` (staged upgrades + timeout semantics) and this file.
2. `clifleet doctor` + `clifleet status` from Mac Studio; confirm SSH to mbp/omarchy/claw1.
3. Decide with Fan: **longer timeout refetch** vs **host-side Xcode / keychain / mise / Node**.
4. Only after timing OK, refetch the lagging hosts/tools, then apply.
5. Optionally cut a real `v0.1.1` (or `v0.2.0`). Release binaries pick up that tag through the existing ldflags. `go install` of a raw commit keeps printing `0.1.0` until `internal/cli.Version` changes.

## Contact / ownership note

Fan asked Indiekit (2026-10-29) to refresh docs/progress and hand maintenance to **another agent**. Update this file when fleet state or `main` tip changes.
