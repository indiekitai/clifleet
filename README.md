# clifleet

**Upgrade your AI CLIs across every machine.**

clifleet SSHes to a list of hosts, sees which AI coding CLIs are actually installed on each one, upgrades those binaries with that machine's own install method, and prints a version matrix. Machines are not the same. A host with Claude and no Codex stays that way.

This is not a single-machine updater ([aiupdate](https://github.com/narze/aiupdate) and [agentup](https://github.com/rockychang7/agentup) already do that) and not a config syncer (ai-sync, mcpocket). It does not route model traffic. [Magpie](https://usemagpie.co/) does that.

## Install

On macOS or Linux (amd64 / arm64), without Go:

```bash
curl -fsSL https://raw.githubusercontent.com/indiekitai/clifleet/main/install.sh | sh
```

It installs the latest release build to `~/.local/bin/clifleet`. `CLIFLEET_INSTALL_DIR` picks another directory and `CLIFLEET_VERSION=v0.2.0` pins a release. The archive must match the release's `SHA256SUMS`. When `gh` is installed and logged in, `gh attestation verify` must also confirm that this repo's release workflow built it (`CLIFLEET_NO_ATTESTATION=1` skips that check). The new binary must run before anything is replaced. Running the same line again upgrades.

To upgrade an installed clifleet:

```bash
clifleet update --check   # print current and latest, change nothing
clifleet update           # replace this binary with the latest release
```

`update` (alias `self-update`) only touches clifleet's own binary, never the CLIs on your hosts; `upgrade` is the one that upgrades the hosts. A clifleet built from source reports `dev` (or the Go pseudo-version `go install` recorded) and is left alone unless you pass `--force`.

In a terminal, `status` and `upgrade` end with one line such as `clifleet v0.3.0 is available (this is v0.2.1). Run clifleet update.` when a newer release exists. The check runs beside the fleet run, asks GitHub at most once a day (once an hour after a failure, cached in `~/.cache/clifleet/update-check.json`), never prints with `--json` or into a pipe, and `CLIFLEET_NO_UPDATE_CHECK=1` turns it off.

With Go:

```bash
go install github.com/indiekitai/clifleet/cmd/clifleet@latest
```

The binary is placed in `$(go env GOPATH)/bin`. Building needs Go 1.22+. `CGO_ENABLED=0` produces a static binary:

```bash
CGO_ENABLED=0 go build -trimpath -o clifleet ./cmd/clifleet
```

Tagged releases (`v0.1.0` and later) are built by `.github/workflows/release.yml` for Linux and macOS, amd64 and arm64. There is no npm package.

The machine you run clifleet on needs an OpenSSH client. The hosts need `sh` and whatever already installed the CLI (the native updater, npm, or Homebrew).

## Quick start

```bash
clifleet init
# edit ~/.config/clifleet/hosts.toml
clifleet doctor
clifleet status
clifleet upgrade --dry-run
clifleet upgrade
clifleet upgrade --one-shot --only agent
```

`clifleet status --dry-run` prints the probe plan and does not connect. `clifleet upgrade --dry-run` does connect, so the command matches the install method on disk, and then it stops before changing anything.

`clifleet upgrade` downloads first and then swaps. The download and the swap are separate steps, one tool at a time, so a terminal opened during the download still sees the old binary. `--fetch` and `--apply` run those two steps on their own. `--one-shot` is the in-place updater for tools that cannot be staged.

## Heterogeneous fleets

Hosts do not carry the same tools. One laptop has Claude and OpenCode. A US server has Claude and Codex and must not grow a China-only CLI such as `dsh`. A lab box already has OpenCode and `dsh` and has no Codex. clifleet does not flatten that.

The global `tools` list is only a list of names to look for. **Presence on the host is what matters.** If `command -v` does not find the binary, the cell is `—` / `not installed`, and `upgrade` skips it. clifleet will not install the missing CLI.

Two optional per-host controls narrow the search further:

- `tools` is an allowlist. It replaces the global candidate list for that host.
- `skip` is a blocklist. It always wins, including over the allowlist.

`--only` narrows again. It cannot add a tool the host policy excluded, so `clifleet upgrade --only dsh` does nothing on a host whose allowlist is `claude` and `codex`.

```toml
tools = [
  "claude", "codex", "opencode",
  "cursor", "agent", "cursor-agent",
  "gemini", "agy",
  "copilot", "amp", "pi", "aider", "goose",
  "openclaw", "hermes", "kimi", "qwen",
]

[[hosts]]
name = "us-server"
ssh = "claw1"                 # Host alias in ~/.ssh/config
tools = ["claude", "codex"]   # do not look for anything else here
# skip = ["dsh", "opencode"]

[[hosts]]
name = "shanghai"
ssh = "dev@shanghai.example.com"
tools = ["claude", "opencode", "dsh"]
```

`dsh` is not in the default candidate list. Naming it on `shanghai` means "look for it here." If it is absent, it stays absent. Opt-in tools already have a built-in upgrade spec, so this host does not need a `[[recipe]]` for an npm install of `@deepseek-ai/dsh`. There is no `dsh update` subcommand. A copy that is not an npm install is skipped until a recipe says how to upgrade that copy. A recipe still does not install it.

## Example fleet file

`clifleet init` writes `~/.config/clifleet/hosts.toml` (or `$XDG_CONFIG_HOME/clifleet/hosts.toml`):

```toml
tools = [
  "claude", "codex", "opencode",
  "cursor", "agent", "cursor-agent",
  "gemini", "agy",
  "copilot", "amp", "pi", "aider", "goose",
  "openclaw", "hermes", "kimi", "qwen",
]

concurrency = 8
timeout = "3m"

[[hosts]]
name = "laptop"
local = true

[[hosts]]
name = "us-server"
ssh = "claw1"
tools = ["claude", "codex"]

[[hosts]]
name = "shanghai"
ssh = "dev@shanghai.example.com"
tools = ["claude", "opencode", "dsh"]

[[hosts]]
name = "workshop"
ssh = "workshop"
skip = ["opencode"]
```

`ssh` is an OpenSSH destination: a `~/.ssh/config` alias, a hostname, or `user@host`. A `:port` suffix works. You can also set `host`, `user`, `port`, and `identity` separately. `local = true` runs on the machine where you invoked clifleet, with no SSH.

Other fields worth knowing:

| Field | Meaning |
| --- | --- |
| `concurrency` | Parallel SSH sessions. Default 8, maximum 64. |
| `timeout` | Per-host budget for `status` and for `upgrade --one-shot`. For the default upgrade, `--fetch`, and `--apply` it is the budget of **each tool**, so one slow `brew fetch` cannot fail the other tools on that host. Default `3m`. `status` uses **45s** when this field is omitted and you do not pass `--timeout`. Bare numbers are seconds. Release and Homebrew downloads inside a fetch use that per-tool budget, minus a few seconds, when the budget is longer than the built-in cap. A 10 minute `--timeout` can finish a large native archive. `npm pack` stays shorter. Each tool is still capped on its own. |
| `path_extra` | Absolute directories prepended to the remote `PATH`. |
| `methods` | Force a channel for one installed tool: `brew`, `npm`, `bun`, `pnpm`, `yarn`, or `native`. Ignored when the binary is missing. |

clifleet uses your `ssh` binary, so `ProxyJump`, agent keys, `Include`, and `Match` keep working. It sets `BatchMode=yes` so a password prompt fails fast instead of hanging. It forces no TTY (`-T`, `RequestTTY=no`) so a host config cannot turn `sh -s` into an interactive prompt. A non-interactive `sh -s` does not read `.zprofile`, so the remote script puts the usual Homebrew directories on `PATH` itself (`/opt/homebrew/bin`, `/usr/local/bin`, and the Linuxbrew paths) and runs `brew shellenv` when `brew` is there. It does that again after nvm, because nvm sometimes replaces `PATH` and drops Homebrew. It does not turn off `StrictHostKeyChecking`.

`clifleet doctor` only runs `true`. A line like "Connection ... port 22 succeeded" means the SSH session opened. `status` and `upgrade` then run a shell script: PATH setup, optional nvm, and `--version` for each candidate. That script can sit after doctor already returned. clifleet caps nvm at 5s and each `--version` at 15s, with stdin on `/dev/null`, so one stuck CLI cannot burn the whole host budget. If that probe times out and the binary lives in Homebrew's Cellar or Caskroom, the version comes from `brew list --versions` instead. Other hosts keep running. If the budget still fires, the row is `unreachable (timed out after ...)` and stderr says whether the probe script started. Narrow a live fleet while you look:

```bash
clifleet doctor --hosts omarchy
clifleet status --hosts omarchy --only claude --timeout 45s
clifleet upgrade --fetch --hosts omarchy --only codex
clifleet upgrade --apply --hosts omarchy --only codex
```

`--hosts` picks machines. `--only` narrows tools and cannot add a name the host allowlist or blocklist already excluded. Neither flag installs a missing CLI.

## Commands

```text
clifleet status
clifleet status --dry-run
clifleet status --hosts us-server,workshop --only claude
clifleet status --json

clifleet upgrade
clifleet upgrade --dry-run
clifleet upgrade --fetch
clifleet upgrade --apply
clifleet upgrade --one-shot --only agent
clifleet upgrade --only codex --hosts us-server

clifleet doctor
clifleet doctor --offline

clifleet history
clifleet history --hosts us-server --only codex
```

`status` prints one row per host and candidate. A host with no allowlist has one row for every default name; this sample is only the first few:

```text
HOST        TOOL      VERSION    METHOD  STATUS
laptop      claude    2.1.44     native  installed
laptop      codex     —          —       not installed
laptop      opencode  1.19.4     brew    installed
us-server   claude    2.1.40     npm     installed
us-server   codex     0.163.0    native  installed
```

`us-server` has no OpenCode row, because OpenCode is not on that host's allowlist. `shanghai` would have a `dsh` row of its own.

## How an upgrade is chosen

clifleet resolves symlinks and reads the path (Homebrew Cellar or Caskroom, `node_modules`, package.json). Then it uses only that channel. The command's stdin is `/dev/null`. Homebrew and npm are pointed at non-interactive settings. `CI` is left unset.

The table is the in-place command (`clifleet upgrade --one-shot`). The default upgrade stages npm, Homebrew, native Codex, and native Claude Code instead of running that command. See the next section.

| Already installed as | One-shot command |
| --- | --- |
| Claude Code, native installer | `claude update` |
| Claude Code, Homebrew cask | `brew upgrade --cask claude-code` (or `claude-code@latest` when that is the cask on disk) |
| Claude Code, npm | `npm install -g @anthropic-ai/claude-code@latest` |
| Codex, standalone installer | `codex update` |
| Codex, Homebrew cask | `brew upgrade --cask codex` |
| Codex, npm | `npm install -g @openai/codex@latest` |
| OpenCode, install script | `opencode upgrade` |
| OpenCode, Homebrew | `brew upgrade` of the formula on disk |
| OpenCode, npm | `npm install -g` of the package on disk (`opencode-ai` or `@opencode/cli`) |

`npm install -g` here is the upgrade for a package that is already installed. It is not used when `command -v` fails. `brew install` and curl installers are not used to add a missing CLI. A cargo install is reported and left alone. A binary with no recipe is skipped with `no upgrade recipe` rather than guessed into an installer.

`clifleet upgrade` uses the staged commands below when the install method has one. It does not run `claude update` or `codex update` unless you pass `--one-shot`.

## Staged upgrades

`codex update` and a live `npm install -g` replace the binary while they download. A new terminal opened in that window can fail. The default upgrade keeps those apart: download everything that can be staged, then do the short swap. Each tool is its own process with its own timeout. One slow `brew fetch` fails only that tool.

```bash
clifleet upgrade
clifleet upgrade --fetch    # download only
clifleet upgrade --apply    # swap only, from the cache fetch wrote
clifleet upgrade --one-shot # in-place updater, for tools marked no-stage
```

`clifleet fetch` and `clifleet apply` are the same two steps. `--hosts`, `--only`, `--timeout`, `--parallel`, `--json`, and `--dry-run` work as they do on `upgrade`. `--dry-run` still connects, prints the command for the install method on disk, and does not run it. `--fetch` and `--apply` together are an error.

Fetch writes a cache on each host at `$XDG_CACHE_HOME/clifleet`, or `~/.cache/clifleet` when `XDG_CACHE_HOME` is unset. It does not replace the live binary. Apply reads that cache. A tool with nothing staged is skipped with `nothing staged`.

| Already installed as | Fetch | Apply |
| --- | --- | --- |
| npm global | `npm pack <pkg>@latest` into the cache | no-op when the staged version is already installed; otherwise `npm install -g --prefix <tmp>` and swap the package directory |
| Homebrew formula or cask | `brew update`, then `brew fetch` into Homebrew's own cache. A failed `brew update` still tries `brew fetch` from the tap already on disk. Xcode or Command Line Tools that Homebrew calls too old are `blocked` with that reason | `brew upgrade` with auto-update off |
| native Codex | `https://releases.openai.com/codex/channels/latest`, then `codex-package-<triple>.tar.gz` for that version. GitHub `rust-v<version>` is the fallback | versioned release directory, then flip the standalone `current` symlink |
| native Claude Code | version from `downloads.claude.ai/claude-code-releases/latest`, then `manifest.json` and `<version>/<platform>/claude`. The binary's SHA256 has to match the manifest | copy into `~/.local/share/claude/versions/<version>` and retarget `~/.local/bin/claude`. The previous version file stays |
| native Cursor Agent (`agent`, `cursor-agent`) | version read from the official `https://cursor.com/install` script, then `downloads.cursor.com/lab/<version>/<os>/<arch>/agent-cli-package.tar.gz` | extract into `~/.local/share/cursor-agent/versions/<version>` and retarget the existing symlink |

A native install is staged only when the resolved binary sits in that vendor's own layout: Codex under a `standalone` package directory, Claude Code under `~/.local/share/claude/versions/`, Cursor Agent under `~/.local/share/cursor-agent/versions/`. A binary that only has the right name, such as an unrelated `agent` or a wrapper script called `claude`, is `no-stage` and is never replaced. Apply checks the layout again on the host and reports `blocked` when the binary on `PATH` has moved out of it since the fetch; Claude Code and Cursor Agent must also be a symlink into their versions directory. Native OpenCode (`~/.opencode/bin`) is `no-stage` too: its npm channel would add a second copy under `npm root -g` rather than upgrade the one on disk.

Codex's native package is the documented GitHub release asset. Claude's native package is the manifest on `downloads.claude.ai`, not an HTML page. Cursor Agent's version comes from the official installer script's `downloads.cursor.com/lab/<version>/...` URL. An unsupported platform is `blocked`, not a guessed download. `claude update` and `agent update` are not used for these installs unless you pass `--one-shot`.

npm apply installs into a temporary prefix first (`--include=optional`, so a platform package such as `@openai/codex-linux-x64` is installed with the wrapper). For `@openai/codex`, fetch also packs `@openai/codex@<version>-<platform>` from whatever registry `npm` is already using. The live tree is moved aside only after that install has produced a package, and it is moved back if `codex --version` does not run. Leftover `@openai/.codex-*` directories from an interrupted npm rename are removed; the live `codex` directory is not. Apply does not run `npm install` at all when the staged tarball's version already matches the package on disk. That row is `up to date`.

If the tarball's `engines.node` is not satisfied (OpenClaw's `>=24.22.1 <25 || >=26.12.1` on Node v22.23.4, for example), fetch reports `blocked (needs Node …)` with the host's Node version. Apply does not run. clifleet does not upgrade Node.

Apply is the short command. For npm the package tarball is already on disk. npm may still fetch a dependency that is not in the npm cache while it fills the temp prefix. For Homebrew, apply still swaps the keg or cask. That swap is the unlink and relink, not the bottle download. If a formula's dependencies were not already cached, brew can download those during the swap. Run apply soon after fetch so the cached bottle still matches the formula.

These native updaters stay `no-stage`. They do not publish a stable artifact URL clifleet can fetch: `opencode upgrade`, `copilot update`, `amp update`, `pi update`, `agy update`, `goose update`, `openclaw update`, `hermes update`, `kimi upgrade`, `qwen update`, `grok update`, `kilo upgrade`, `kiro-cli update`, and `aider --upgrade`. `clifleet upgrade` leaves them alone and says so. `clifleet upgrade --one-shot` runs the updater in place. That is the path that can make the binary disappear while it downloads.

Bun, pnpm, and yarn global installs are `no-stage` as well. They do not have a pack command as direct as `npm pack`.

A missing tool is still skipped. Fetch does not install it. A binary with no recipe, including the Cursor editor shim, is still `no upgrade recipe`. `dsh` stays opt-in. A cargo install is still left alone.

A `mise`, `asdf`, or Volta shim is not an install target, and neither is a tool inside a manager's own `installs/` (mise, asdf) or `~/.volta/tools/image/` tree. `command -v` can hit a shim that resolves to the `mise` binary (`/usr/bin/mise`); apply refuses that with `managed by mise` and does not run `mise use` or replace the version manager. Shims are appended after `~/.local/bin`, so a real binary there is the one that gets swapped.

Apply does not unlock the macOS login keychain. After the staged files are in place, a `--version` that only reports `Your macOS login keychain is locked`, prints no version, or times out leaves the new symlink in place. A version token that does not match the staged release is still rolled back, and so is a binary that cannot execute (exit 126 or 127) or dies on a signal. Codex retargets its previous `current` release (or removes `current` if there was none); Claude Code and Cursor Agent retarget the previous symlink. A link that cannot be put back is reported as `rollback failed`, never as restored. If the live link already points at the staged version, apply reports it current and does not rewrite the files it runs from.

The footer counts tool rows. `failed` adds `(N hosts)` when that host count is different, so three failed tools on one machine are `3 failed (1 host)` rather than `1 failed`.

`status` and `upgrade` write `$XDG_CACHE_HOME/clifleet/last-report.json`, or `~/.cache/clifleet/last-report.json` when `XDG_CACHE_HOME` is unset. That file is the durable copy of a long run. Each `upgrade`, `fetch`, or `apply` that is not `--dry-run` also appends one JSON line per host and tool to `history.jsonl` in the same directory; `clifleet history [--hosts LIST] [--only LIST] [--limit N] [--json]` reads it back, so "when did codex change on us-server" or "which host failed last time" has an answer. Tools that were not installed are not recorded. If you pipe the table through `tee`, turn on `pipefail` so `tee`'s success does not hide a failed upgrade:

```bash
set -o pipefail
clifleet upgrade 2>&1 | tee "$HOME/.cache/clifleet/upgrade.log"
```

## Built-in catalog

Three tiers. Only the first is searched on every host.

| Tier | What it is | How it joins a host |
| --- | --- | --- |
| Default | Curated worldwide CLIs in `DefaultTools()` | The fleet file's `tools` list, or this set when `tools` is omitted |
| Opt-in | Built-in upgrade spec, not a default candidate | Name it in `tools` (global or that host's allowlist) |
| Recipe | No built-in spec | A `[[recipe]]` plus a `tools` entry. The recipe does not add the name by itself |

`cursor`, `agent`, and `cursor-agent` are three PATH names. `cursor` is the editor shell command (it can fail when Cursor.app is absent). `agent` and `cursor-agent` are the Cursor Agent CLI. A native install under `~/.local/share/cursor-agent/versions/<version>` is staged from the official lab tarball and the symlink is retargeted. A Homebrew cask is still `brew upgrade`. `--one-shot` runs `agent update` or `cursor-agent update`. A machine with both symlinks retargets each one that is already installed.

| Tool | Tier | Already installed as | Command |
| --- | --- | --- | --- |
| Claude Code, Codex, OpenCode | default | see the table above. Native Codex and Claude Code also stage from the GitHub release asset; `upgrade` fetches that and then swaps | see the table above |
| `cursor` | default | Homebrew cask | `brew upgrade --cask cursor` |
| `cursor` | default | editor shim on `PATH` | skipped, no native updater |
| `agent` | default | install script | staged lab tarball, then retarget the symlink. `--one-shot` is `agent update` |
| `cursor-agent` | default | install script | same staged tarball. `--one-shot` is `cursor-agent update` |
| `cursor-agent` | default | Homebrew cask `cursor-cli` | `brew upgrade --cask cursor-cli` |
| `gemini` | default | npm `@google/gemini-cli` | `npm install -g @google/gemini-cli@latest` |
| `gemini` | default | Homebrew formula `gemini-cli` | `brew upgrade gemini-cli` |
| `gemini` | default | anything else | skipped. The cheatsheet mentions `gemini update`; the FAQ and command reference do not, so it is not called |
| `agy` | default | Antigravity CLI | `agy update` |
| `copilot` | default | native / npm `@github/copilot` / cask `copilot-cli` | `copilot update`, or that package manager |
| `amp` | default | native, or npm | `amp update`, or `@ampcode/cli` (`@sourcegraph/amp` when that path is on disk) |
| `pi` | default | native or npm `@earendil-works/pi-coding-agent` | `pi update` (the CLI only, not `pi update --all`) |
| `aider` | default | pip/uv | `aider --upgrade --yes-always` |
| `aider` | default | Homebrew formula `aider` | `brew upgrade aider` |
| `goose` | default | script install | `goose update` |
| `goose` | default | Homebrew | `brew upgrade block-goose-cli` (not the unrelated `goose` formula; a Cellar token on disk wins) |
| `openclaw` | default | its own updater | `openclaw update --yes` |
| `hermes` | default | managed source checkout | `hermes update` |
| `kimi` | default | native or npm `@moonshot-ai/kimi-code` | `kimi upgrade --yes`, or npm |
| `qwen` | default | native or npm `@qwen-code/qwen-code` | `qwen update`, or npm |
| `dsh` | opt-in | npm `@deepseek-ai/dsh` | `npm install -g @deepseek-ai/dsh@latest` |
| `dsh` | opt-in | anything else | skipped. No `dsh update` subcommand |
| `grok` | opt-in | native or npm `@xai-official/grok` | `grok update`, or npm |
| `kilo` | opt-in | native or npm `@kilocode/cli` | `kilo upgrade`, or npm |
| `kiro-cli` | opt-in | Kiro CLI | `kiro-cli update --non-interactive` |

Left as a user recipe, not a built-in: MiniMax Code (sources disagree on the binary, `mcode` versus `minimax`), and the rest of the [Magpie](https://usemagpie.co/) agent list. [aiupdate](https://github.com/narze/aiupdate) also refreshes skills with `npx skills update`. That is out of scope here.

`hermes update` does not rewrite a Store, Nix, Docker, or Termux APT install. `openclaw update` refuses system-package installs. Those copies are not given a guessed `brew` token.

After a successful command, clifleet probes the version again. An unchanged version is `up to date`, not an error.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Every selected host completed. Missing tools are successes. |
| 1 | Bad arguments, or the fleet file could not be read. |
| 2 | A host was unreachable, or an upgrade command failed. |

Other hosts still run. One failure does not hide the rest of the matrix.

## Comparison

| | clifleet | aiupdate, agentup | Magpie | ai-sync, mcpocket |
| --- | --- | --- | --- | --- |
| Job | Upgrade AI CLI binaries across a fleet | Upgrade AI CLIs on this machine | Local model gateway and per-agent model picker | Sync MCP servers and settings |
| Machines | Many, over SSH | One | One | Many, but the files, not the binaries |
| Missing CLIs | Left uninstalled | Skipped on that machine | Not what it does | Not what they do |
| API keys, model config | Untouched | Untouched | The product. Gateway on `127.0.0.1:3425` | Often the point |

Use aiupdate or agentup when you are sitting at the one machine you want to update. Use Magpie when you want Claude Code or Codex to call a different model. Use clifleet when the same kind of binary upgrade has to happen on a list of hosts that are not alike.

The default catalog follows those upgrade/status peers where their commands match upstream, and it stops there:

| Peer | What clifleet took from it |
| --- | --- |
| [aiupdate](https://github.com/narze/aiupdate) | `claude update`, `codex update`, `opencode upgrade`, `cursor-agent update`, `copilot update`, `pi update` (same self-update as their `pi update pi`), `agy update`. Skills via npx are not a clifleet tool. |
| [agentup](https://github.com/rockychang7/agentup) | npm and Homebrew tokens for Codex, Claude Code, and OpenCode. Agy's upgrade is the native `agy update` from Antigravity's own docs, which is newer than agentup's "upgrade manually" note. |
| [agent-cli-updater](https://github.com/Yunz93/CLI-updater) | Gemini CLI, Cursor, Kimi, Qwen Code, Amp, Copilot, Aider, alongside Codex, Claude Code, and OpenCode. |
| [code-aide](https://github.com/dajobe/code-aide) | `agent` as the Cursor CLI command, plus claude, gemini, opencode, kilo, amp, codex, copilot. |
| [sync-ai-clis](https://github.com/hyeonbungi/sync-ai-clis) | Update commands for claude, codex, gemini, `kiro-cli`, and agy. That project installs missing tools after consent. clifleet does not. |
| [CC Switch](https://ccswitch.io/) | Name list only (Claude, Codex, Gemini, Grok, OpenCode, OpenClaw, Hermes, Pi, MiniMax, Kimi, Antigravity, and others). Install hints are not copied. |
| [Magpie](https://usemagpie.co/) | Long-tail awareness only. Those names stay a user `[[recipe]]`. |

## Agent skill

[`skills/clifleet/SKILL.md`](skills/clifleet/SKILL.md) tells a coding agent how to run clifleet on your fleet: `status` and `--dry-run` first, ask before a real upgrade, `--fetch` then `--apply`, read `last-report.json`, and do not work around `not installed` or `managed by mise`. Copy or link the directory into your agent's skills folder, for example `ln -s "$PWD/skills/clifleet" ~/.claude/skills/clifleet`. A test fails if the skill names a command or flag that `--help` does not have.

## Development

```bash
go test ./...
go vet ./...
CGO_ENABLED=0 go build -o clifleet ./cmd/clifleet
```

GitHub Actions (`.github/workflows/ci.yml`) runs those on every push. See [CONTRIBUTING.md](CONTRIBUTING.md).

## Maintainer

Operational fleet state and progress for maintainers live in [docs/HANDOFF.md](docs/HANDOFF.md). Product docs stay in this README and [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE)
