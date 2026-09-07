# Documentation size bands

A repo declares one band and gets its caps: lines and chars per Markdown file,
how many `docs/*.md` reference pages it may carry, and the roomier but scarcer
pair for `guides/*.md` narrative walkthroughs.

| band | lines | chars | docs | guide lines | guide chars | guides |
| --- | --- | --- | --- | --- | --- | --- |
| `small` | 40 | 3,000 | 20 | 80 | 6,000 | 3 |
| `large` | 120 | 8,000 | 40 | 240 | 16,000 | 6 |

```toml
[tool.agentic-os.documentation-layout]
band = "small"
```

Every repo declares, small included. There is no default to fall into: an
undeclared repo and a deliberately small one would otherwise be the same file,
and only one has had the decision made. A repo outgrowing `small` should hit a
cap and argue for `large`, not find out it was never on a band. A typo fails the
same way a missing declaration does. Non-Python repos use `.agentic-os.toml`.

## guides/ is the narrative shelf

`docs/` answers *how does X work*, capped short and flat so the set stays
scannable. `guides/` answers *how do I do Y, end to end*, where the value is the
sequence, the worked example and the failure modes, none of which survive being
cut to reference length. A guide takes twice its band's per-doc caps and a
deliberately scarce count, both derived from the band so a band move carries
them. The two shelves count separately, the case that forced the type: a repo at
its `docs/` cap can still add a guide. Opt in by creating the directory, and
cross-link out as `../docs/<name>.md`, since `dead-cross-links` resolves a
relative link against the file it sits in.

## Why a count cap exists at all

A per-doc size cap does not bound a docs folder, it reshapes it. A repo that
caps length and not count answers every over-long doc by splitting it, and the
folder grows without any single file failing. `sirens-echo` was the proof: 156
docs, median 2,935 chars, largest 3,989 against a 4,000 cap, and unreadable.

## Why lines bind before chars

Measured across the fleet, Markdown here runs about 49 characters per line. So
40 lines is roughly 1,960 characters and 120 lines is roughly 5,880. The char
cap sits above both on purpose: it is the backstop that catches a doc dense with
tables or code, not the everyday constraint. One set near the line cap's natural
size would fire constantly on prose; one far above would never fire at all.

## The two caps multiply

Count times lines is a total documentation budget, and it is the number worth
arguing about rather than either cap alone: `small` is 20 x 40 = 800 lines,
`large` is 40 x 120 = 4,800, and the guide shelf adds 30% on top of its band's
budget by construction. A repo cannot escape the budget by trading one cap
against the other. Merging two docs to clear the count spends the line cap, and
splitting one to clear the line cap spends the count.

## No per-file escape

`excludes` still governs placement and flatness, and no longer reaches either
size cap or the count. A generated file that lands over the cap is a generator
emitting too much, and the fix is the generator. The rule this replaced allowed
a per-file exemption, and they accumulated where the pressure was highest: one
repo excluded nine `SKILL.md` files, two excluded `docs/FEATURES.md` from the
cap that exists to keep it an inventory.

## A vendored tree is not this repo's prose

A repo may declare `vendored` path prefixes under the hook config. Markdown
beneath one takes no size cap, because its shape is owned outside this repo: an
SDK the repo vendors, or copy an external surface renders. Prefixes only, never
a bare basename, which would exempt one filename everywhere. It answers the size
cap alone, so it cannot widen where Markdown may live. `size_excludes` is the
sibling key for prose a repo owns but sizes differently, such as a monorepo
co-locating a README and `docs/` under each component: `excludes` governs
placement and never reaches the size caps, so without it such a repo could buy
the exemption only by calling its own docs vendored.

## The org profile README belongs to the forge

`<org>/.github/profile/README.md` takes no size cap and no module-README shape
from this hook. Both forges render it as the organisation's front page, so the
module-README remedy of "move the body into a `docs/*.md` file" would blank it.
It sat outside every fleet rollout until the walker saw a `.github` repo at all.

## Skill entrypoints belong to check-skills

`SKILL.md` and `COMPOSED.md` take no size cap from this hook. `check-skills`
owns them through `categories.yaml`, which allows 500 lines and 10,000 bytes.
Sharing one cap made a skill pass the validator that owns skills and fail the
one that owns layout, and the failure told the author to split a skill into
`docs/`. A skill does not overflow there. It overflows into its own
`references/`, which `check-skills` deliberately leaves uncapped. **Two hooks
disagreeing about one file is a defect in the suite, not a decision an author
can act on.** So the exemption covers a skill's `references/` tree as well as
the two entrypoint basenames. `check-skills` answers an over-long `SKILL.md`
with "move detail into a sibling `references/` file", and a band cap on that
sibling failed the author for following the remedy the suite gave them.

## Every carve-out, in one place

Markdown lives at the root allowlist, in a flat `docs/` or `guides/`, or in a
skill dir, and takes its band's caps. `docs/FEATURES.md` is not special. The
carve-outs are the sections above plus these:

- **Gitignored paths and `SKIP_DIR_NAMES`** - never scanned
  ([why](build-output-is-not-content.md)).
- **`excludes`** - placement and flatness only. It stopped reaching either size
  cap, so an entry here no longer answers an over-long file.
- **`SIZE_CAP_EXEMPT_BASENAMES`** - `CODE_OF_CONDUCT.md`, verbatim upstream,
  plus the two skill entrypoints. **`examples/`** - any `*.md` under one, at
  any depth, the Go and Rust idiom.
- **Size opt-ups** - `agents_md_max_*` and `readme_max_*` lift `AGENTS.md` and the root `README.md` off their defaults.

## Baseline

- `docs/` and `guides/` must stay flat - no subdirectories, use filename prefixes (`features-*.md`, `aterm-*.md`, `skill-discipline-*.md`).
- A nested `SKILL.md` below a top-level skill dir fails: the loader only sees top-level dirs.
- A co-located **module `README.md`** is allowed in one of two capped shapes, each <= 3 non-blank lines (blank lines free, prose lines <= 90 chars). **Outpost** - a heading, optional one-sentence summary, and exactly one link to a single `docs/*.md` file that must link back to that exact README path (reciprocal, file-to-file). One doc may anchor many outposts. **Homestead** - heading plus up to 2 content lines, no `docs/` pointer (self-contained signage). The discriminator is whether the README links a `docs/*.md` file. This turns the per-repo `ansible/README.md` / `deploy/*/README.md` excludes into a rule - a conforming README needs no exclude.
