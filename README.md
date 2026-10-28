# clifleet

**Upgrade your AI CLIs across every machine.**

clifleet SSHes to a list of hosts, sees which AI coding CLIs are actually installed on each one, upgrades those binaries with that machine's own install method, and prints a version matrix. Machines are not the same. A host with Claude and no Codex stays that way.

This is not a single-machine updater ([aiupdate](https://github.com/narze/aiupdate) and [agentup](https://github.com/rockychang7/agentup) already do that) and not a config syncer (ai-sync, mcpocket). It does not route model traffic. [Magpie](https://usemagpie.co/) does that.

## Install

From a checkout:

```bash
git clone https://github.com/indiekitai/clifleet.git
cd clifleet
go install ./cmd/clifleet
```

Or, once this module is public:

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
clifleet upgrade --only claude
```

`clifleet status --dry-run` prints the probe plan and does not connect. `clifleet upgrade --dry-run` does connect, so the command matches the install method on disk, and then it stops before changing anything.

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
| `timeout` | Per SSH session. Default `3m`. Bare numbers are seconds. |
| `path_extra` | Absolute directories prepended to the remote `PATH`. |
| `methods` | Force a channel for one installed tool: `brew`, `npm`, `bun`, `pnpm`, `yarn`, or `native`. Ignored when the binary is missing. |

clifleet uses your `ssh` binary, so `ProxyJump`, agent keys, `Include`, and `Match` keep working. It sets `BatchMode=yes` so a password prompt fails fast instead of hanging. It does not turn off `StrictHostKeyChecking`.

## Commands

```text
clifleet status
clifleet status --dry-run
clifleet status --hosts us-server,workshop --only claude
clifleet status --json

clifleet upgrade
clifleet upgrade --dry-run
clifleet upgrade --only codex --hosts us-server

clifleet doctor
clifleet doctor --offline
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

clifleet resolves symlinks and reads the path (Homebrew Cellar or Caskroom, `node_modules`, package.json). Then it runs only that channel's upgrade. The command's stdin is `/dev/null`. Homebrew and npm are pointed at non-interactive settings. `CI` is left unset.

| Already installed as | Command |
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

`npm install -g` here is the upgrade for a package that is already installed. It is not used when `command -v` fails. `brew install` and curl installers are not used at all. A cargo install is reported and left alone. A binary with no recipe is skipped with `no upgrade recipe` rather than guessed into an installer.

## Built-in catalog

Three tiers. Only the first is searched on every host.

| Tier | What it is | How it joins a host |
| --- | --- | --- |
| Default | Curated worldwide CLIs in `DefaultTools()` | The fleet file's `tools` list, or this set when `tools` is omitted |
| Opt-in | Built-in upgrade spec, not a default candidate | Name it in `tools` (global or that host's allowlist) |
| Recipe | No built-in spec | A `[[recipe]]` plus a `tools` entry. The recipe does not add the name by itself |

`cursor`, `agent`, and `cursor-agent` are three PATH names. `cursor` is the editor shell command (it can fail when Cursor.app is absent). `agent` is the Cursor Agent CLI from the official install script (`agent update`). `cursor-agent` is the same agent binary under the name Homebrew's `cursor-cli` cask links, which [aiupdate](https://github.com/narze/aiupdate) updates with `cursor-agent update`. A machine with both symlinks runs that updater once per name.

| Tool | Tier | Already installed as | Command |
| --- | --- | --- | --- |
| Claude Code, Codex, OpenCode | default | see the table above | see the table above |
| `cursor` | default | Homebrew cask | `brew upgrade --cask cursor` |
| `cursor` | default | editor shim on `PATH` | skipped, no native updater |
| `agent` | default | install script | `agent update` |
| `cursor-agent` | default | install script | `cursor-agent update` |
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

## Development

```bash
go test ./...
go vet ./...
CGO_ENABLED=0 go build -o clifleet ./cmd/clifleet
```

GitHub Actions (`.github/workflows/ci.yml`) runs those on every push. See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE)
