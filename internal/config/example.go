package config

// Example is the fleet file written by `clifleet init`.
// Tools are candidates. Nothing in this file is installed automatically.
const Example = `# clifleet fleet
# Upgrade your AI CLIs across every machine.
#
# "tools" is a candidate list, not an install list. status reports what is
# already on each host. upgrade updates only those binaries. A missing CLI
# is shown as not installed and left alone — including tools that are fine
# on one machine and unwanted on another.
#
# Three tiers:
#   default   the names below (a curated worldwide set)
#   opt-in    built-in upgrade spec, but not searched unless a host lists it
#             (dsh, grok, kilo, kiro-cli)
#   recipe    anything else. A [[recipe]] describes an upgrade. It does not
#             add the name to this list and it does not install the binary.
#
# Per-host "tools" is an allowlist (it replaces the global candidate list).
# Per-host "skip" is a blocklist. skip wins over the allowlist.
# Regional CLIs such as dsh stay off the global list. Add them only on the
# hosts that already use them.
#
# cursor is the editor shell command. agent and cursor-agent are the Cursor
# Agent CLI under two PATH names (the install script links both; Homebrew's
# cursor-cli cask links cursor-agent). They are not the same binary as cursor.

tools = [
  "claude", "codex", "opencode",
  "cursor", "agent", "cursor-agent",
  "gemini", "agy",
  "copilot", "amp", "pi", "aider", "goose",
  "openclaw", "hermes", "kimi", "qwen",
]

concurrency = 8
# Per-host session budget for upgrade, fetch, and apply. Omit it and status
# uses 45s instead of 3m. A single CLI --version or nvm.sh is capped inside
# the probe either way, so one stuck command cannot sit for this whole budget.
timeout = "3m"

[[hosts]]
name = "laptop"
local = true

[[hosts]]
name = "us-server"
ssh = "claw1" # Host alias in ~/.ssh/config
tools = ["claude", "codex"] # do not look for anything else here
# skip = ["dsh", "opencode"]

[[hosts]]
name = "shanghai"
ssh = "dev@shanghai.example.com"
# identity = "~/.ssh/id_ed25519"
# port = 22
# This machine may already have a regional CLI. clifleet still will not
# install it; dsh is only upgraded when the binary is already on PATH.
tools = ["claude", "opencode", "dsh"]

[[hosts]]
name = "workshop"
ssh = "workshop"
skip = ["opencode"]

# dsh is opt-in. Listing it above looks for the binary; it does not install
# it. An npm install of @deepseek-ai/dsh is upgraded from the built-in spec.
# There is no dsh update subcommand, so a copy with no package metadata is
# skipped until a recipe says otherwise.
#
# A recipe overrides a built-in spec or teaches an upgrade for a tool that
# has none. It still does not install the tool.
# [[recipe]]
# name = "some-tool"
# native = ["some-tool", "update"]
`
