# Removing a validator rule

Deleting a rule from the catalog suite is three lines of code and a long tail
of prose that is now wrong. This is how to find the tail, and why the obvious
search misses the part that matters most.

The worked example throughout is the `guides/` count cap, removed on
2026-09-07. It is the case that taught every step below, including the step
that was skipped.

## Read the page that introduced the rule, first

Before any search, open the document that introduced the rule and read it top
to bottom, once. Not grep it. Read it.

For the count cap that was `guides/writing-a-guide.md`, 117 lines, about a
minute. That minute would have found a section arguing the cap was low on
purpose, which four separate greps did not.

Do this first because it is cheaper than the searching, and because a rule's
own introduction is where its rationale is densest.

## Then search four ways, not one

The symbol search is the one everybody runs and the one that finds least.

1. **The symbol.** The constant, the function, the check's registration in
   whatever assembles the suite, and its test coverage.
2. **Derived values.** Anything computed from the constant. The count cap fed
   a generated caps-reference row and a "30% on top of its band's budget"
   figure that was `count x per-file / band budget`. Both are false the moment
   the constant goes, and neither mentions the symbol.
3. **The rationale words.** How the rule was argued: `scarce`, `scarcity`,
   `on purpose`, `deliberately`, `why the cap`. Prose that justifies a rule
   rarely names the identifier that implements it.
4. **The bare value.** A rationale usually restates the number while the code
   names the symbol, so grep the literal too. The section that survived every
   other search said "six" and made a claim about what a repository asserts by
   having six of something.

## What the count cap actually cost

Six sites came out of the symbol and derived-value searches: the constant, its
accessor, the check, its registration, its tests, and two generated rows.

A seventh came from the Developer Advocate reading the page, after the removal
had already landed and been called complete. Four sentences arguing that
scarcity was the point, in the document that teaches the type. All four false,
none greppable by symbol, none by value except the one that said "six".

Prose that argues for a rule is invisible to the search that finds everything
else, and it sits closest to the reader most likely to believe it.

## Removing is not the only outcome

A cap that fires is not automatically wrong. The count cap was wrong because
its premise was wrong: it assumed guide count tracks how much narrative a repo
invented, when it tracks how many things a repo ships, so agent-compose hit it
by having eight seats.

A cap that is deliberate, documented, and now binding is a different case, and
the answer there is displacement rather than deletion. That decision belongs to
whoever owns the content, not to whoever owns the hook.

## The defect worth checking for before you touch the number

A validator that fails a repo while naming a remedy that repo cannot take is
broken regardless of whether its threshold is right. The count cap told
agent-compose to fold guides into `docs/`, which was already at its own cap.

That shape recurs. When a hook fires, read its remedy and ask whether the repo
it fired on could actually follow it. If not, the remedy is the bug.

## After it lands

Removing a rule loosens a constraint, so nothing fails to tell you the sweep
was incomplete. Verify by the consumer rather than by your own suite: the repo
that hit the rule should now pass, and somebody there should confirm it rather
than you inferring it from a release number.

## See also

- [../docs/documentation-bands.md](../docs/documentation-bands.md) - the bands,
  the caps that remain, and the budget reasoning behind them.
- [writing-a-guide.md](writing-a-guide.md) - what belongs on this shelf.
