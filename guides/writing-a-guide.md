# Writing a guide

[`documentation-bands`](../docs/documentation-bands.md) carries the caps, the
band table and the cross-link rule. This page is the other half: how to tell,
before you start writing, whether the thing in your head is a guide or a
reference page.

Getting it wrong is cheap in one direction and expensive in the other. A guide
filed as reference gets cut until the part that mattered is gone. A reference
page filed as a guide sits on a deliberately scarce shelf and crowds out
something that needed the room.

## The test

**Cut it to reference length in your head. Does the value survive?**

A reference page loses nothing by being short and scannable. That is what it is
for. Somebody arrives knowing what they want, reads the two paragraphs that
answer it, and leaves.

A walkthrough loses the sequence, and the sequence was the content. If the
useful part is "first this, then that, and here is what goes wrong in the
middle," then trimming it to a summary produces a page that is technically
accurate and helps nobody.

If you cannot tell, ask who is reading it and what they are doing while they
read. A guide has a reader who is **doing something, in order**. Reference has a
reader who is **looking something up**.

## The failure that produced the type

Worth keeping because it is the clearest case, and because the remedy text was
part of the problem.

A narrative walkthrough was written for a project whose `docs/` sat at exactly
its count cap. It was refused three ways at once: the folder was full, the page
was over the line cap, and it was over the char cap. Moving it to `guides/`
failed too, because `guides/` was not yet a legal location.

The count cap was the interesting refusal. **The page was not too long. The
shelf was full and the page was not that kind of page.** A repo can be genuinely
finished adding reference pages and still owe its readers a walkthrough.

The remedy text at the time said to split the doc into smaller `docs/*.md`
files. Followed literally, that advice destroys a walkthrough: you get three
reference fragments and no sequence. It now says something else, and the reason
that matters is that a validator's suggested fix is read as authoritative by
whoever hit it at midnight.

## What guides/ is not

**It is not overflow for a full `docs/`.** This is the failure most available to
you, and it is available precisely when you are frustrated.

The same evening the type shipped, a comment cap forced explanatory prose out of
two configuration files. The obvious destination doc was at exactly its line
cap, and `docs/` was at its count cap. `guides/` had free slots. Putting
configuration commentary there would have satisfied every validator and been
wrong, because nobody reads it in order and nobody is doing anything while they
read it. The prose went onto the tracker record instead, and the shelf stayed
for pages that earn it.

Three shapes that look like guides and are not:

* **A tour of a subsystem.** If it has no reader goal, it is architecture
  reference with narration attached.
* **A long changelog or migration note.** Sequential, but the reader is not
  performing the sequence.
* **A design rationale.** Valuable, and it belongs beside the thing it explains
  rather than on a shelf a reader consults while working.

## What an over-cap guide usually means

A guide that will not fit is rarely a guide that needs splitting. It is almost
always a guide that **grew a reference section**.

Look for the block that answers "how does X work" rather than "do this next."
That block is a `docs/` page, and lifting it out usually takes the guide back
under cap on its own while making both halves better. The guide then links to it
as `../docs/<name>.md`.

If nothing lifts out and it is still over, the walkthrough is covering two
tasks. Split it by task rather than by length, and accept that both halves cost
a slot on a deliberately scarce shelf.

## Being scarce is the point

The count cap is low on purpose, and low relative to the docs count rather than
in absolute terms.

If guides outnumber reference pages, something has gone wrong upstream: either
the reference set has been hollowed out into narration, or tasks are being
documented that nobody performs. A repository with six guides is a repository
claiming six things are worth walking somebody through end to end, and most
repositories do not have six.

Treat a full guides shelf the way you would treat a full docs shelf. It is a
signal to merge or retire, never a reason to raise a cap.

## Cross-linking out

`dead-cross-links` resolves a relative link against the file it sits in, so a
bare `foo.md` inside a guide resolves to `guides/foo.md` and fails. Reference
`docs/` as `../docs/<name>.md`.

This is the one mechanical thing that catches everybody once. It fails loudly,
which is the good case.

## This page is the first one

Said plainly because it is relevant rather than cute. The type was specified by
the seat whose page could not land, built by the seat that owns the validators,
and the argument for it is a failure that happened rather than a shape somebody
liked. If the distinction here reads as tidier than your actual situation, trust
your situation. The test at the top is the whole of it, and everything below is
consequence.
