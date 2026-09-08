# Handbook: Categories

Pick the prefix up front. The validator rejects unknown prefixes.

* `<personal>-*` (e.g. `kai-*`) - operating context - durable rules about how the user works (preferences, voice, git workflow, repo registry pointers).
* `ops-social-gws-*` - Gmail family - verb-shaped children plus an `ops-social-gws-shared` parent.
* `ops-social-google-*` - Calendar family.
* `ops-eng-sentry-*` - Sentry review playbooks.
* `ops-investigation-*` - investigation playbooks and runbooks. Status-enforced. Required H2 sections enforced.
* `gaming-eco-*` - Eco modding (investigation, scaffolding, source-auditing).
* `writing-*` - prose and issue authoring surface (`tooling-issue-triage` and
  the social writing family). The voice family ships from voice-corpus.
* `personality-*` - role-neutral presence, attention, tempo, and voice for agent-compose personality providers.
* `home-*` - smart-home control at My House (hue, sonos, cast).
* `tooling-*` - agent-ecosystem meta (the scout family,
  `coding-core-supply-chain-audit`, and role-specific methods). Meta-tooling may
  stay in the personal prefix when it encodes operating-context discipline.
* `coding-*` - code-engineering recipes (Discord bot scaffolding, the coding-shape-iac umbrella, git/GitHub PR workflow). Reusable build patterns, not tooling on the agent ecosystem itself.

Exact-name skills (don't fit a prefix):

* `ops-investigation` - router across all `ops-investigation-*` skills.
* `<ops-investigation-meta>` - meta-discipline router (cross-cutting investigation rules).
* `skill-creator` - this skill (handbook + authoring loop).
* `gaming-steam` - Steam library (one-off).
* `gaming-factorio` - placeholder for future Factorio work.
* `ward-passthroughs` - symlink into `ward's skills dir`. Single source of truth lives in the ward repo; this name is registered as an exact-name skill in the personal-OS repo so the validator recognizes it without owning its content. Symlinks are skipped from validation but their names are recognized for cross-link resolution.

Picking a category for a new skill:

* A new investigation playbook for a user-system component goes to `ops-investigation-*`.
* An Eco-game-server failure investigation goes to `gaming-eco-investigation`. The `ops-investigation` router cross-links it.
* A new shape that doesn't fit any of the above: **stop and update this handbook + `categories.yaml` first**, then create the skill. The validator rejects unknown prefixes by design.
