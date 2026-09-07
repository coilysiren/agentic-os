# Claude credentials and settings

A native session home is session-scoped, and Claude Code keys its macOS Keychain
credential to that home. Without a bridge the harness starts logged out on every
launch. This page records why, and what the launcher does about it.

## Why the Keychain cannot carry it

Claude Code namespaces the Keychain item by a digest of `CLAUDE_CONFIG_DIR`. The
default config directory keeps the bare service name `Claude Code-credentials`,
and any other directory takes a suffix of the first four bytes of the SHA-256 of
the exact path string.

Every native session mints a fresh id, so `CLAUDE_CONFIG_DIR` differs on every
launch, which yields a service name that has never been written. The harness
reports no error. It simply asks the operator to log in.

## What the launcher does

It seeds one file and lets the symlink farm do the rest.

Claude Code reads `.credentials.json` from `CLAUDE_CONFIG_DIR` in preference to
the Keychain, and falls back to the Keychain only when that file is absent. So
at session creation, when the harness is Claude, the launcher writes the
Keychain login to `~/.claude/.credentials.json` if that file does not exist.
[The configuration projection](native-harness-config.md) already links every
entry of `~/.claude` into the session home, so the credential is carried by the
same mechanism as everything else and needs no code of its own.

Seeding never overwrites. Once the file exists it is authoritative and the
Keychain item stops being updated, so copying the stale item over a live file
would retire the token every session is using.

## Write-back at cleanup

A rotated token reaches the canonical file two ways, because it goes missing two
ways. A session that **replaced** the link with a regular file has that file
copied back. A session whose entry is **gone** has its Keychain item read
instead, because the harness deletes `.credentials.json` once the token behind
it expires and falls back to the item digested from `CLAUDE_CONFIG_DIR`
(`teable:coilyco-flight-deck/agentic-os#7021`, reproduced under plain `claude`).
Without that second case the seed cannot recover, since it never overwrites: an
expired canonical stays expired and every seat pays a login every launch.

Only a token that **outlives** canonical is written, compared on
`claudeAiOauth.expiresAt`, so an unparsable or unstamped payload loses rather
than winning as zero. That is what separates this from the lend-and-return
failure below. Failure warns, never blocks.

## Boundaries and tradeoffs

The seed is macOS only, because only macOS keeps the credential outside the
config directory. Linux already stores it in the file the projection carries,
and Windows credential storage is not wired up here.

Concurrent refreshes are the vendor's problem rather than this launcher's. Every
session resolves the same path, so Claude Code's own concurrent-refresh handling
coordinates them, which is what the previous lend-and-return arrangement was
reimplementing badly: it kept one Keychain item per session and wrote back at
reap, so with several live sessions the last harvest won and every earlier
rotation was discarded.

No secret crosses argv. The seed reads the Keychain through
`/usr/bin/security find-generic-password` and writes the file at `0600`, so the
deliberate argv exception this page used to document is gone with the writes
that needed it.

If the canonical file is ever deleted, the next launch reseeds from the Keychain.
That value may be old enough to be refused, which costs one login, the same as
having no credential at all.

## Settings guardrails

Back to [features-agents.md](features-agents.md).

The fleet guardrails that live in `~/.claude/settings.json`. Both are authored
here and converged by the `claude-hooks` ansible role in `infrastructure`, per
the authoring-vs-rollout rule in [AGENTS.md](../AGENTS.md).

## Fleet permission rules

`scripts/apply-base-claude-settings.py` appends to `permissions.deny` and
`permissions.allow` and removes only the two `RETIRED_*` lists, so operator
rules and the sibling `ask` / `defaultMode` keys survive, and a rerun no-ops.

Two shut, none open:

* **Live-infrastructure CLIs** - `gcloud`, `kubectl`, `helm`, `terraform`,
  `gsutil`, `mongosh`, `mongo`. Each mutates production or a database, so it
  belongs to an operator or a guarded `aosguard ops` verb, not a raw agent
  shell. The deny is what steers an agent to the guarded surface.
* **Harness memory directory** - `Edit` against
  `**/.claude/projects/**/memory/**`, one rule that binds Write, Edit,
  MultiEdit, and NotebookEdit. `autoMemoryEnabled: false` stops the harness
  writing memory files, and the deny stops an agent authoring one by hand.

`BASE_ALLOWED_PERMISSIONS` is empty. An allow rule must name the tool it widens,
so agentic-os#1165's bare `*` only ever warned at startup and is now retired.

`effortLevel` is deliberately not a fleet key. It tunes latency and spend per
host, which makes it operator-local preference under the config-placement axes,
so it stays hand-edited and no writer owns it.

## Fleet preference

`tui: fullscreen` is the one preference the writer sets beside the guardrails,
because Kai chose the fullscreen renderer as the fleet default rather than a
per-host tuning. A host whose terminal cannot take the alternate screen, such as
iTerm2 under `tmux -CC` or a screen reader, exports `CLAUDE_CODE_NO_FLICKER=0`
in `~/.shellrc.local`: the env var outranks the saved key, so convergence keeps
writing the default and the host keeps ignoring it.

## Read-only assertion

`agentic-os-kai/scripts/up-to-date.py` asserts the remaining guardrails are
present and reads the deny rules from `BASE_DENIED_PERMISSIONS` rather than
restating them.
It never writes, so a failure means the host needs convergence.
