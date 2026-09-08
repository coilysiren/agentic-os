# Documentation size bands

A repo declares one band and gets its caps: lines and chars per Markdown file,
how many `docs/*.md` pages it carries, and the roomier pair for `guides/*.md`.

| band | lines | chars | docs | guide lines | guide chars |
| --- | --- | --- | --- | --- | --- |
| `micro` | 40 | 3,000 | 5 | 80 | 6,000 |
| `small` | 40 | 3,000 | 20 | 80 | 6,000 |
| `large` | 120 | 8,000 | 40 | 240 | 16,000 |

`micro` shares small's per-file caps and cuts the count to 5, for a repo whose
real content lives elsewhere, usually in skills.

```toml
[tool.agentic-os.documentation-layout]
band = "small"
```

Every repo declares. An undeclared repo and a deliberately small one would
otherwise be the same file, and only one had the decision made. A typo fails
like a missing declaration. Non-Python repos use `.agentic-os.toml`.

**The declaration is a request, not the decision.** A band applies only where it
matches the repo's entry in the policy file, copied by hand into both repos, so a
repo outgrowing `small` argues there rather than raising its own caps.
Disagreement falls it back to `small`:
[ratifying a band or an exclusion](../guides/ratifying-an-exclusion.md).

## guides/ is the narrative shelf

`docs/` answers **how does X work**, capped short and flat. `guides/` answers
**how do I do Y, end to end**, where the sequence, the worked example and the
failure modes do not survive being cut to reference length. A guide takes twice
its band's per-doc caps, the shelves count separately, and a guide cross-links
out as `../docs/<name>.md`. There is no guide count cap, which is why **opening
a shelf is ratified rather than a mkdir**: an uncapped destination is where
content goes when it fits nowhere else. Two repos carry one today.

## Why a count cap exists at all

A per-doc size cap does not bound a docs folder, it reshapes it: a repo that
caps length and not count answers every over-long doc by splitting it.
`sirens-echo` was the proof, at 156 docs, none over a 4,000-char cap, unreadable.

## Why lines bind before chars

Markdown here runs about 49 characters per line, so 40 lines is roughly 1,960
characters and 120 lines roughly 5,880. The char cap sits above both as the
backstop for a doc dense with tables or code, not the everyday constraint.

## The two caps multiply

Count times lines is a total documentation budget for `docs/`, and it is the
number worth arguing about rather than either cap alone: `small` is 20 x 40 =
800 lines, `large` is 40 x 120 = 4,800. Merging two docs to clear the count
spends the line cap, and splitting one to clear the line cap spends the count.

## No per-file escape

`excludes` still governs placement and flatness, and no longer reaches either
size cap or the count. A generated file that lands over the cap is a generator
emitting too much, and the fix is the generator. The rule this replaced allowed
a per-file exemption, and they accumulated where the pressure was highest: one
repo excluded nine `SKILL.md` files, two excluded `docs/FEATURES.md` from the
cap that exists to keep it an inventory.

## A vendored tree is not this repo's prose

A repo may declare `vendored` path prefixes. Markdown beneath one takes no size
cap, because its shape is owned outside this repo: an SDK the repo vendors, or
copy an external surface renders. Prefixes only, never a bare basename, which
would exempt one filename everywhere. It answers the size cap alone, so it
cannot widen where Markdown may live.

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
- **`band`, `excludes` and `size_excludes`** - requests rather than grants. A pattern
  applies only when it appears both in the repo's own config and in this repo's
  `agentic_os/documentation_policy.yaml`, and an unratified local pattern
  fails the hook by name rather than silently not applying. `excludes` reaches
  placement and flatness only, never either size cap. `vendored` is not
  ratified, since it asserts provenance rather than asking for an exemption.
  Both directions: [ratifying an exclusion](../guides/ratifying-an-exclusion.md).
- **`SIZE_CAP_EXEMPT_BASENAMES`** - `CODE_OF_CONDUCT.md`, verbatim upstream,
  plus the two skill entrypoints. **`examples/`** - any `*.md` under one, at
  any depth, the Go and Rust idiom.
- **Size opt-ups** - `agents_md_max_*` and `readme_max_*` lift `AGENTS.md` and the root `README.md` off their defaults.

## Baseline

- `docs/` and `guides/` must stay flat - no subdirectories, use filename prefixes (`features-*.md`, `aterm-*.md`, `skill-discipline-*.md`).
- A nested `SKILL.md` below a top-level skill dir fails: the loader only sees top-level dirs.
- A co-located **module `README.md`** is allowed in one of two capped shapes, each <= 3 non-blank lines (blank lines free, prose lines <= 90 chars). **Outpost** - a heading, optional one-sentence summary, and exactly one link to a single `docs/*.md` file that must link back to that exact README path (reciprocal, file-to-file). One doc may anchor many outposts. **Homestead** - heading plus up to 2 content lines, no `docs/` pointer (self-contained signage). The discriminator is whether the README links a `docs/*.md` file. This turns the per-repo `ansible/README.md` / `deploy/*/README.md` excludes into a rule - a conforming README needs no exclude.
