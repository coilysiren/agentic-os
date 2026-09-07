# Features

Major shipped capabilities, not files.

## Inventory

- [Shell and secrets](install.md) - shared shells, SSM, and GPG.
- [Branded agent terminal](aterm.md) - `aterm` opens one composed agent session in its own Sombra kitty
  window, completing and refusing roles against the live roster, and writes a macOS `.app` launcher per
  [role bundle](aterm-bundles.md), each carrying its own embedded icon. The window opens on an identity card drawn from the
  overlay's own `geometry`, standing over that role's own
  [creature background](aterm-creature.md), and sounded from its `sound_mark` when `--sound` asks.
  `aterm pane on` and `off` [split that window](aterm-pane.md) beside a command and put it back,
  re-deriving the creature rather than depending on anything the split recorded.
  Mac and Linux only, since kitty has no Windows build.
- [Speech helper](aos-roles-and-voice.md) - `aos-say` client plus relay for status speech.
- **Karabiner key bindings** - external keyboard and Remote Desktop mappings.
- [Agents and sessions](features-agents.md) - self-name, composition
  status, harness policy, and
  [settings guardrails](native-claude-credentials.md).
- [Native agent workspaces](native-agent-workspaces.md) - role-scoped worktrees,
  leases, cleanup, the `aos` temp namespace, and standalone launches.
  [Shadow lifecycle](native-shadow.md) verbs list, release, and reap them.
- [Fail-closed launch provenance](native-session-start.md) - native startup
  fetches each policy source, verifies the digest Agent Compose sealed into the
  repository plan, regenerates once on a mismatch, and stops before any worktree
  exists when that cannot converge.
- [Agent-compose provider](context-budget.md) - scoped skills,
  personality, and eight deployed roles across the Agent Compose v3 roster.
- [Agent tool evaluation](../.agents/skills/tooling-agent-tool-evaluation/SKILL.md) - cross-harness tool evals.
- [Role-composed skills](role-composed-skills.md) - v2 Core Roster method slices.
- [AOS launcher](aos-cli.md) - role context with
  [convergence](aos-convergence.md), [connectivity](aos-context-bundle.md),
  [kubeconfig](aos-cluster-access.md), [issue pins](aos-issue-flow.md), and
  [check-ins](aos-issue-flow.md).
- [aos run](aos-cli.md) - `aos run <verb>` resolves a just verb to the resident repository
  declaring it, reports how far that checkout trails its upstream, and `--handoff` prints the
  absolute-path line to run outside a session shadow.
- [aosguard](../.agents/skills/tooling-aosguard/references/aosguard.md) - guarded CLI with PR
  merge and sealed
  [Forgejo storage measurement](../.agents/skills/tooling-aosguard/references/forgejo-ops.md).
- [Guarded helm releases](../.agents/skills/tooling-aosguard/references/guardfile-headers.md) - cluster-pinned upgrade, install, and rollback with release destruction unexposed.
- [Code review skill](../.agents/composed/tooling-code-review/COMPOSED.md) - Portfolio Director gate-decision review stance.
- [Code review contract](../CODE-REVIEW.md) - review invariants.
- [Forgejo Actions logs](../.agents/skills/tooling-aosguard/references/forgejo-actions-runs.md) - job logs and run ZIPs.
- [Forgejo runner tokens](../.agents/skills/tooling-aosguard/references/forgejo-ops.md) - guarded registration-token minting.
- [Forgejo policy vendoring](vendor-forgejo-policy.md) - the operator policy pushes down to deploy, never fetched up.
- [Homebrew updates](../.agents/skills/tooling-aosguard/references/guardfile-headers.md) - `aosguard update brew` refreshes tap metadata, upgrades this estate's own formulae first, then the rest, and exits non-zero on anything left behind.
- [Teable schema admin](../.agents/skills/tooling-aosguard/references/teable-admin.md) - guarded field and table creation that re-reads through a separate request and refuses unless the write stored as asked; convert and table-delete refused by name.
- [Teable personal records](../.agents/skills/tooling-aosguard/references/teable-personal.md) - guarded record reads and writes over one SSM-pinned base that the caller cannot name; writes re-read before reporting success, and record-delete is unmounted and refused by name.
- [Ward integration boundary](ward-specs.md) - one generic runner for every
  [composed role](aos-roles-and-voice.md), and no role-derived authority.
- [Cross-repo tooling and release](release.md) - aos-precommit and release operations.
- [Telegram CI failure alerts](../actions/telegram-alert/action.yml) - one composite action, no alert program in any repo.
- [dev-base image](dev-base-image.md) - parallel cached language payloads feeding one automatically released full development surface.
- [Pinned WASM toolchain](dev-base-wasm-toolchain.md) - wasm-pack, wasm-opt, and the wasm-bindgen CLI baked so no build downloads them.
- [CI parity in dev-base](ci-in-dev-base.md) - CI runs inside the moving :release dev-base image.
- [Pull-request CI gate](ci-in-dev-base.md) - fast tests and Docker-only image validation.
- [AGENTS pointer](features-agents.md) - generated sibling-repo workspace pointer.
- [AGENTS git-workflow block](git-workflow-lanes.md) - generated per-lane standing authorization to commit, branch, push, and open a PR. One fleet lane, `pull-request-and-merge`.
- **Brand case** - the brand name is lowercase in prose, sentence-initial
  included. Fenced blocks, code spans, URLs and link targets are exempt, and a
  literal external identifier takes an `allow` entry rather than an edit.
- [Encoded leak guard](pre-commit-hygiene.md) - hex-encoded leak-term detector.
- [Outbound link hygiene](pre-commit-hygiene.md) - offline validator for links
  leaving the repo, driven by a retired-name and retired-path table, plus a
  report-only liveness CLI for a scheduled job.
- [Managed line endings](pre-commit-hygiene.md) - generated `.gitattributes`
  block pinning the working tree to LF, with vendored trees and vendor orgs
  left alone.
- [Context measurement](context-budget.md) - reusable harness-neutral role
  capture, multi-provider attribution, and deterministic component diffs.
- [AGENTS inventory](agents-context-inventory.md) - fleet corpus and clipping candidates.
- [Repository residency](repo-layout.md) - Agent Compose native-workspace adapter and status tracking.
- [Catalog caps reference](catalog-caps-reference.md) - generated numeric caps for validators.
- [TLS trust-store fallback](../.agents/skills/tooling-aosguard/references/tls-trust-store.md) -
  operator verbs and repo modules load a system CA bundle when the interpreter
  ships without one, instead of failing every HTTPS call closed with an error
  that reads like a bad endpoint. Verification is never weakened.
- [Narrative guides shelf](documentation-bands.md) - `guides/*.md` as a second
  structural documentation type beside `docs/*.md`, with its own roomier size
  caps and a scarce count, so an end-to-end walkthrough has somewhere legal to
  live when the reference shelf is full.
- [Canonical agent-id generator](build-output-is-not-content.md) - short lowercase agent IDs.
- [Agent-compose provider](context-budget.md) - the AOS capability provider contract.

## See also

- [README.md](../README.md) - human-facing intro.
- [AGENTS.md](../AGENTS.md) - public-safe agent operating rules.
- [justfile](../justfile) - dev verbs.
- [.ward/ward.yaml](../.ward/ward.yaml) - catalog metadata only.

Cross-reference convention from [release.md](release.md).
