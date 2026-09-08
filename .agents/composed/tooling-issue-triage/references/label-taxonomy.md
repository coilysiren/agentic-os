# Label taxonomy: three scoped axes

Every triage label is a `prefix/value` pair, and Forgejo treats a prefixed group as exclusive when the group is declared so. That moves "exactly one per issue" from a convention agents must remember into a property the tracker enforces.

| axis | exclusive | values |
| --- | --- | --- |
| `priority/*` | yes | `P0` `P1` `P2` `P3` |
| `autonomy/*` | yes | `headless` `live-collab` `async-consult` `epic` |
| `role/*` | no | `platform` `sysadmin` `science` `frontend` `gamedev` `director` `advocate` `analyst` `human` |

`role/*` is deliberately not exclusive, because one issue can legitimately need two seats. A decision with a landing carries both - the seat that rules and the seat that applies the result.

`role/human` is the one value that is not a seat. It means a person is required and specifically not an agent seat, so reach for it only when no seat fits.

## The 2026-09-01 deletion of `priority/P4`

`priority/P4` was the icebox: the demotion sink for parked, speculative-but-kept
work that should stay open and tracked rather than be closed. It was deleted
because it never did that.

Measured across the whole fleet on 2026-09-01:

* **21 open, 1,196 closed.** It was the most-applied priority label in the estate by lifetime volume.
* **Median lifetime 14 days**, filing to close. 37% closed within a week, and only 10% lived past 30 days.
* **53% of all its closes landed on three days**, 73% on five.

So nothing parked. Issues were marked `priority/P4` to stage a bulk close and
were closed within a fortnight, mostly in batches. The label was a marshalling
step for closure wearing the name of a resting place.

That mismatch was load-bearing rather than cosmetic. [target-shape](target-shape.md)
set a **30-50% stock target** on a tier whose behaviour was **flow**, so its
3.7% standing balance - the ordinary residue between burn-downs - read as a
failure to prune. In one 2026-09-01 session that reading produced three
successive wrong conclusions before the closed-issue count settled it.

Deleting the tier rather than retuning its band was Kai's call, made against a
recommendation to keep the label and drop only the band. The cost she accepted
is stated plainly: **deleting a Forgejo label strips it from every issue
carrying it**, so the 1,196 closed issues lost their marker and
`state:closed label:priority/P4` stopped being a queryable record of five past
burn-downs.

Nothing replaced it. `priority/P3` is now both the default and the floor, and
the prune is closing, which reopen already makes reversible. See
[pruning-and-api](pruning-and-api.md).

## The 2026-08-15 rename

The taxonomy replaced a flat set. Old spellings match nothing, so write the full name.

```
P0..P4       ->  priority/P0..priority/P4
consult      ->  autonomy/async-consult
headless     ->  autonomy/headless
interactive  ->  autonomy/live-collab
IRL          ->  role/human
                 autonomy/epic          new
                 role/*                 new
```

`role/*` arrived in two rounds. The first shipped `ai`, `creator`, `design`,
`engineer` and `qa`, and `role/human` inherited everything else. That put a
director's decisions and an operator's actions in one pile, so `ops`,
`director` and `exec` followed and `role/human` was retuned from "must
physically do something" to "a person, and specifically not an agent seat".
Every one of those spellings was retired in the 2026-08-25 rename below.

Two entries are semantic rather than spelling. `live-collab` means a human has to be **present**, where `interactive` described a mid-flight checkpoint. And `async-consult` names the wait as asynchronous: a question in a queue, not an appointment.

## The 2026-08-25 rename onto Core Roster v2

The values above named the eight-seat roster that Core Roster v2 replaced, so no
label existed for any of the seven seats that do exist. The seats moved and the
labels did not, and nothing reported it.

```
role/engineer  ->  role/platform
role/ops       ->  role/sysadmin
role/ai        ->  role/science
role/director  ->  role/director
role/creator   ->  role/advocate
role/design    ->  role/frontend
                   role/gamedev     new, no predecessor
role/exec          retired, folded into role/director
role/qa            retired, issues redistributed
role/human         unchanged, never a seat
```

Each rename was an edit in place, so all 770 issues kept the label they already
carried and none was touched. `role/gamedev` starts empty, and the game and mod
work that predates the seat sits on `role/platform` until the sweep at
[agent-compose#345](https://forgejo.coilysiren.me/coilyco-flight-deck/agent-compose/issues/345)
finds it.

The whole change is at
[agent-compose#342](https://forgejo.coilysiren.me/coilyco-flight-deck/agent-compose/issues/342).

## This page describes, it does not define

The taxonomy is declared in
[`config/forgejo-label-taxonomy.yaml`](https://forgejo.coilysiren.me/coilyco-bridge/infrastructure/src/branch/main/config/forgejo-label-taxonomy.yaml)
in infrastructure, and converged from there.

That split is new, and it is the fix for what produced the drift above. The
authority used to be live Forgejo org state in one org, which a script copied
outward, so a label change was an API call with no diff and no commit. This page
sat describing a set that had moved, with nothing able to notice. Add a value
here without adding it there and it does not exist.

## Why `role/*` exists

`autonomy/async-consult` says a human is needed. It never said **which** human, so a director's decision queue and an operator's action queue arrived as one undifferentiated pile, and neither owner could see their own work. Tagging the seat splits them without inventing another autonomy value.

Backfilling one org showed the cost concretely. Twenty-nine issues landed on
`role/human` before the later values existed; eighteen turned out to be
operator actions and eleven were decisions. Once every one of them had a real
seat, `role/human` emptied. A value that holds everything unassigned tells you
nothing about whose queue an issue is in, which is the only question this axis
exists to answer.

## Why `autonomy/epic` sits in the autonomy group

An epic is not an autonomy ceiling and does not belong on that scale. It is in the group because the group is exclusive, and that exclusivity is the mechanism: an epic cannot also be `autonomy/headless`, so nothing dispatches it as a single task. Dispatch its children, which carry their own ceilings.

## Renaming these labels is a breaking change

Anything that matches on the label **string** breaks silently on a rename. The dispatch gate is the load-bearing case (see [automation-mode-axis](automation-mode-axis.md)), but doc-content tests and triage scripts match too.

The failure mode is the dangerous kind: nothing errors. Every issue stops matching, everything falls to the fail-closed default, and the burndown queue quietly empties while looking healthy. Change the labels, the matchers, and these docs in one batch.

That warning is load-bearing for `priority/*` and `autonomy/*`, which are matched
by string in a dispatch gate, a doc-content test, two AGENTS files, a worklog,
and a migration script. It does not reach `role/*`, which the 2026-08-25 rename
established is matched nowhere outside this page. Check before assuming which
case you are in, because the two carry very different costs.
