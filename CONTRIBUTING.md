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

Built-in recipes live in `internal/catalog`. There are three tiers:

1. **Default.** `DefaultTools()` and the `tools` list in `internal/config/example.go`. A curated worldwide set. Add a name here only when most makers might already have it and the upgrade command is verified.
2. **Opt-in.** A `builtin` spec, not in `DefaultTools()`. Use this for regional CLIs (`dsh`) and tools that are real but not part of the default search (`grok`, `kilo`, `kiro-cli`). A host that already has one lists it in `tools`.
3. **User recipe.** Everything else, including the long tail of model-router catalogs. A recipe only describes how to upgrade a binary that is already installed. It does not add the name to the candidate list:

```toml
[[recipe]]
name = "some-tool"
native = ["some-tool", "update"]
```

Do not add regional or rename-prone tools to the default candidate list.
Hosts that already have them can name them in their own `tools` allowlist.
`dsh` is the example: it is opt-in, its npm package is `@deepseek-ai/dsh`,
and it has no `dsh update` subcommand. Do not invent one.

Leave a channel blank when the upgrade command is unclear. Status can still
report the binary. Upgrade skips it with `no upgrade recipe`.

`cursor` (editor shim), `agent`, and `cursor-agent` (Cursor Agent CLI) are
separate tools. Do not collapse them.

Upgrade commands must be non-interactive. The remote script closes stdin and
exports `NONINTERACTIVE=1`. Do not set `CI=1`; some of these CLIs change
behavior when they think they are in CI. Prefer the upstream updater over a
guessed `brew` or `npm` token. Do not add an installer (`brew install`, a
curl script, `npm install` of a binary that is not already on `PATH`).

`doctor` runs `true`. `status` and `upgrade` run `sh -s` with no TTY. nvm
and each `--version` are capped and must not read the script's stdin. A
timeout has to say whether that script started. Do not source a login shell
or a profile from the probe.

`upgrade --fetch` then `--apply` downloads into `$XDG_CACHE_HOME/clifleet`
(or `~/.cache/clifleet`) and only then replaces the binary. npm is
`npm pack` followed by `npm install -g` of that tarball. Homebrew is
`brew fetch` followed by `brew upgrade`. Native updaters are `no-stage`:
do not add a release-page scraper. A staged command is still not an
installer for a missing binary.

## Tests that should stay

- Host parsing, allowlists, and blocklists.
- Upgrade command selection for native, npm, and Homebrew.
- Fetch/apply plans: npm and Homebrew stage, native updaters are `no-stage`.
- A missing tool never produces an install command, including under `--fetch`.
- `--only` cannot widen a host allowlist.
- `dsh` (and the other opt-in names) stay out of `DefaultTools()`.

Fleet and ops state for a live install is in [docs/HANDOFF.md](docs/HANDOFF.md). This file stays the product rule.
