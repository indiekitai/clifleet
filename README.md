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
tools = ["claude", "codex", "opencode"]

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

`dsh` is not in the default candidate list. Naming it on `shanghai` means "look for it here." If it is absent, it stays absent. A recipe can say how to upgrade it once it is installed; a recipe still does not install it:

```toml
[[recipe]]
name = "dsh"
native = ["dsh", "update"]
```

## Example fleet file

`clifleet init` writes `~/.config/clifleet/hosts.toml` (or `$XDG_CONFIG_HOME/clifleet/hosts.toml`):

```toml
tools = ["claude", "codex", "opencode"]

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

`status` prints one row per host and candidate:

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

## Development

```bash
go test ./...
go vet ./...
CGO_ENABLED=0 go build -o clifleet ./cmd/clifleet
```

GitHub Actions (`.github/workflows/ci.yml`) runs those on every push. See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE)
