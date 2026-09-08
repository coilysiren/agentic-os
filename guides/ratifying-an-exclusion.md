# Ratifying a band or an exclusion

Adding an exclusion to `documentation-layout` takes two pull requests in two
repositories, a hook release, and a pin bump. Removing one takes a single pull
request on either side. This walks both directions and explains why they are
deliberately not the same cost.

## Why it is two pull requests

An exclusion used to live only in the consumer repo's own `pyproject.toml`.
That put the escape hatch inside the repo under pressure, decided by whoever
was blocked at the time, which is the worst possible place for it. A pull
request that adds a doc and a pull request that exempts that doc from the rule
it broke are the same pull request, reviewed by the same nobody.

The failure this prevents is specific and observed. `size_excludes` was added
in #1144 to unblock one repo, spread to seven repositories in under three
weeks, and by then covered a repo's own `docs/` shelf and two bare file paths,
which is the per-file escape #1108 had removed by name. No single change in
that sequence looked wrong from inside the repo making it.

So the grant moved here, to `agentic_os/documentation_policy.yaml`, and the
repo-local declaration stayed. **A pattern takes effect only when it appears in
both.** Neither half is sufficient, and that is the whole mechanism.

## The asymmetry is the design

Adding: two repos, a release, a pin bump. Removing: one pull request, either
side, effective immediately.

An exclusion that is easy to add and easy to remove drifts upward, because the
pressure to add one arrives with a deadline attached and the pressure to remove
one never arrives at all. Making removal the cheap direction is what keeps the
list shrinking without anyone running a campaign.

## Adding one

1. **Try not to.** Read the remedy the hook printed and check whether the repo
   can actually follow it. Splitting an over-long doc into `docs/*.md` is
   usually available and usually better, and a doc split for cap arithmetic is
   a bad trade only when the content that overflows is the part a reader came
   for. If it is, name that in the reason.
2. **Open the agentic-os pull request.** Add or extend the repo's entry in
   `agentic_os/documentation_policy.yaml`. Every entry carries a `reason`
   string, and the test suite fails an entry with an empty one. Write what the
   content is and why the shape the caps assume does not describe it. "Over the
   cap" is not a reason, it is the symptom.
3. **Release the hook.** The `aos-precommit-v*` train advances on a promoted
   diff touching an installed hook input, which this is. The consumer cannot
   see the ratification until it ships.
4. **Open the consumer pull request.** Bump the pin in
   `.pre-commit-config.yaml`, then declare the same pattern under
   `[tool.agentic-os.documentation-layout]`. Declaring it before the pin lands
   fails the hook, which is correct: the grant is not there yet.

## Removing one

Delete it from either side and it stops applying. Removing the central entry
while the repo still declares it turns the repo red with a message naming the
pattern, which is the intended prompt to clean up the local declaration too.
Removing the local declaration first is silent and always safe.

## What an unratified declaration does

It fails the hook. It does not quietly stop working.

```
FAIL: size_excludes pattern 'services/**' is not ratified for this repo. Add it
to agentic_os/documentation_policy.yaml in agentic-os with a written
reason, release the hook, and bump this repo's pin. Until then it grants
nothing.
```

Silently dropping the pattern would trade one silent escape for another. The
repo's config would read as excluded, the hook would disagree, and the next
person to read the `pyproject.toml` would believe the config. A control that
passes quietly instead of refusing is the failure mode
[`tooling-boundary-conformance`](../.agents/skills/tooling-boundary-conformance/SKILL.md)
catalogues, and this hook is not allowed to have it.

## It fails closed

A missing, unreadable, or malformed contract ratifies nothing. It never becomes
a blanket grant. If you have deleted the JSON and every repo went red, that is
the intended direction of failure.

## The band is ratified the same way

`band` is a single value rather than a list, so ratification is agreement
rather than intersection: the repo declares one, the registry names one, and
the caps apply only when they match. A disagreement falls the repo back to
`small` and reports both values, so a repo cannot widen its own caps by editing
one line of its own config.

## The policy lives in two repos, copied by hand

`agentic-os/agentic_os/documentation_policy.yaml` and
`agentic-os-kai/data/documentation-policy.yaml` are byte-identical, and the
linter runs in both. Any difference fails, naming the file.

**No script writes either copy.** That is the point rather than an oversight.
Raising a limit takes two hand edits in two repos in two pull requests, and
doing one without the other turns both repos red instead of passing quietly. A
sync script would collapse that back into one edit, which is the shape this
mechanism exists to prevent.

## The two exclusion keys are ratified separately

`excludes` governs placement and flatness. `size_excludes` answers the size
caps. Ratifying one never grants the other, which is the distinction #1108 drew
and #1144 blurred by making the second key look like a sibling of the first.

`vendored` is not ratified here, because it makes a claim about provenance
rather than asking for an exemption: it says the Markdown's shape is owned
outside this repo. A false `vendored` declaration is a lie in a tracked file
rather than an escape hatch, and it is caught by reading it.

## See also

- [../docs/documentation-bands.md](../docs/documentation-bands.md) - the bands,
  the caps, and every carve-out in one place.
- [removing-a-validator-rule.md](removing-a-validator-rule.md) - what to do
  when the answer is that the rule itself is wrong.
