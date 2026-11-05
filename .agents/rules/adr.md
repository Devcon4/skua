---
description: "An ADR records a choice, not a design; no file:line in the Decision"
alwaysApply: true
---
# Architecture decision records

Full standard, template and worked example: `skill://adr`. A repo whose `docs/adr/README.md`
carries its own template and index uses that template.

An ADR records a choice between options, why that option won, and what reversing it would
cost. It is not a specification. The code is the record of how the system works, and a
record that describes the code competes with the code and goes stale.

Before writing one, check it is a decision at all. If undoing it is a rename, a flag flip
or a refactor with no user-visible change, it is a convention and belongs in `AGENTS.md`.

The one hard limit: zero `file:line` citations in the Decision Outcome block, counting
Consequences and Confirmation. Put them in `More Information`, or leave them out. This is
what catches specification, because a record cannot describe implementation without naming
it.

Length has no cap, and shorter is better. The calibration is a human-written corpus of
nine records for a complex application: 316 words on average, and the longest is 456. A
record that runs past 456 words is specifying, not deciding.

Do not write to a number either. When this corpus was stripped under a 450-word cap,
eleven of twenty-five records landed within five words of it, which is padding dressed as
compliance.

Open the Context on the concrete thing that forced the decision, not on the industry.
"Enterprise applications frequently require seamless, automatic redirection" says nothing a
reader of this repo needs.

Show a code or payload sketch where it teaches faster than a paragraph.

An accepted record changes only by supersession. Typos, link fixes, and removing
specification are the three exceptions. The decision, its reasoning and its consequences
are never edited.

Records that predate this standard stay as they are. New records follow it. Rewriting an
older corpus is its own task, done only when the dev asks.

Write it in the voice of `plain-voice`: flat declaratives, no em-dashes, no rhetorical
cadence. An ADR is read years later by someone deciding whether to undo it.
