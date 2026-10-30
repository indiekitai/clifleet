---
name: clifleet
description: Check or upgrade the AI coding CLIs (Claude Code, Codex, OpenCode, Cursor Agent, …) already installed on a fleet of machines with clifleet. Use when asked which versions each host runs, to upgrade them, or why an upgrade failed.
---

# clifleet

clifleet probes and upgrades AI CLIs that are already installed on each host in
`~/.config/clifleet/hosts.toml`, over `ssh` (or local `sh`). It never installs a
missing tool.

## Rules

1. **Read-only first.** Start with `clifleet status` (or `clifleet doctor` if a
   host is unreachable). `status`, `doctor`, `history`, and any `--dry-run` are
   safe to run without asking.
2. **Ask before a real upgrade.** `upgrade`, `upgrade --fetch`, and
   `upgrade --apply` change hosts, and replacing a CLI can interrupt sessions
   that are using it. Show the user what `clifleet upgrade --dry-run` would do
   and wait until they confirm the timing.
3. **Fetch, report, then apply.** Prefer `clifleet upgrade --fetch` first, report
   what staged and what failed, and run `clifleet upgrade --apply` only when the
   user says so. `fetch` and `apply` are short forms of the same flags.
4. **No `--one-shot` unless asked.** It runs each vendor's in-place updater, which
   can leave a binary missing mid-download. Rows marked `no-stage` have no staged
   path; leave them and tell the user.
5. **Narrow retries.** Re-run only what failed with `--hosts` and `--only`; raise
   `--timeout` for a slow download instead of repeating the whole fleet.
6. **Read the report, not the scrollback.** After a long run, read
   `~/.cache/clifleet/last-report.json` (or `$XDG_CACHE_HOME/clifleet/`). Use
   `--json` for parsing. When piping through `tee`, `set -o pipefail` first.
   Every `status` and `upgrade` (dry runs too) overwrites that file, so copy
   it before running anything else; `clifleet history` keeps past real runs.
7. **Do not work around refusals.** `not installed`, `managed by mise`,
   `blocked (needs Node …)`, and `keychain locked` are deliberate. Do not install
   the tool, edit a mise/asdf shim, bump Node, or unlock a keychain; report the
   host-side fix to the user.

## Question → command

| Question | Command |
| --- | --- |
| What runs where, and what is behind? | `clifleet status` (tool × host matrix, `↑` is behind; `--long` for one row per host and tool; `--json` to parse) |
| Can I reach every host? | `clifleet doctor` |
| What would an upgrade do? | `clifleet upgrade --dry-run` (only rows that would act; `--long` shows all) |
| Download without replacing anything | `clifleet upgrade --fetch` |
| Swap in what was fetched | `clifleet upgrade --apply` |
| One host or tool only | `clifleet upgrade --fetch --hosts NAME --only TOOL --timeout 20m` |
| When did a version change, what failed last time? | `clifleet history --hosts NAME --only TOOL` |
| Update clifleet itself | `clifleet update --check`, then `clifleet update` |

Exit codes: 0 all done (a missing tool still counts as done), 1 bad arguments or
fleet file, 2 an unreachable host or a failed upgrade. One failing host never
hides the others; read every row.
