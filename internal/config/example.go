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
# Per-host "tools" is an allowlist (it replaces the global candidate list).
# Per-host "skip" is a blocklist. skip wins over the allowlist.
# Regional CLIs such as dsh stay off the global list. Add them only on the
# hosts that already use them.

tools = ["claude", "codex", "opencode"]

concurrency = 8
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

# A recipe says how to upgrade a tool that is already installed.
# It does not put the tool on the candidate list and it does not install it.
# [[recipe]]
# name = "dsh"
# native = ["dsh", "update"]
`
