# Contributing

clifleet upgrades AI coding CLIs that are already installed on each machine.
It does not install a CLI just because the fleet file lists it.

## The rule

A missing tool is a skip. `status` prints `—` and `not installed`. `upgrade`
does not run `brew install`, `npm install` of a binary that is not on `PATH`,
or a curl installer.

Per-host `tools` is an allowlist. Per-host `skip` is a blocklist and wins.
`--only` narrows that set. It must not add a tool the host policy excluded,
so a one-off flag cannot put a regional CLI on a machine that does not list it.

If a change would install something that was not already there, it is wrong.

## Develop

Go 1.22 or newer.

```bash
go test ./...
go vet ./...
CGO_ENABLED=0 go build -o clifleet ./cmd/clifleet
```

## Adding a tool

Built-in recipes live in `internal/catalog`. Prefer a user recipe when the
tool is not one of the defaults. A recipe only describes how to upgrade a
binary that is already installed:

```toml
[[recipe]]
name = "dsh"
native = ["dsh", "update"]
```

Do not add regional or vendor-specific tools to the default candidate list
(`claude", `codex`, `opencode`). Hosts that already have them can name them
in their own `tools` allowlist.

Upgrade commands must be non-interactive. The remote script closes stdin and
exports `NONINTERACTIVE=1`. Do not set `CI=1`; some of these CLIs change
behavior when they think they are in CI.

## Tests that should stay

- Host parsing, allowlists, and blocklists.
- Upgrade command selection for native, npm, and Homebrew.
- A missing tool never produces an install command.
- `--only` cannot widen a host allowlist.
