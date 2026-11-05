---
name: adr
description: "Write or revise an architecture decision record in docs/adr/. Use when a choice would be expensive to reverse, when asked to document why something was built a certain way, or when deciding whether something is an ADR at all rather than a convention. Holds the significance test, the template, the length and citation limits, the anti-patterns, and the supersession rules."
argument-hint: "The decision to record, e.g. 'why saves are versioned per chunk'"
---

# Architecture decision records

An ADR records a choice between options, why that option won, and what reversing it would
cost. It is not a specification. The code is the record of how the system works.

A record that describes the code competes with the code and loses, because the code changes
and the record does not. One log reached 21000 words across 26 records that way. A
human-written log for a more complex application held 2851 words across 9.

If the repo's `docs/adr/README.md` carries a template, an index, or its own calibration,
that file wins. Otherwise use the template below.

## Is it an ADR at all

Both gates must pass. Otherwise it is a convention and belongs in `AGENTS.md` or the
repo's durable docs.

1. It affects structure, a non-functional characteristic, a dependency, a published
   interface, or a construction technique.
2. At least two of: high value or risk, such as data loss, lockout or credential exposure;
   a stakeholder's concern, such as the people consuming an API, operating the system, or
   authoring content with it; an unusual quality requirement; an external dependency
   outside the team's control; a cross-cutting effect on every feature; first of its kind
   in the repo; troublesome in the past.

Reversal gate: if undoing it is a rename, a flag flip, or a refactor with no user-visible
change, it is not an ADR. A deletion default that, once flipped, destroys data nobody can
get back passes this gate.

## Limits

One hard limit: zero `file:line` citations in the Decision Outcome block, counting
Consequences and Confirmation. They go in `More Information` as a bare path, or nowhere.
This is what catches specification, because a record cannot describe implementation without
naming it.

Length has no cap, and shorter is better. The calibration is a human-written corpus of nine
records: 316 words on average, and the longest is 456. If a record runs past 456 words, it
is specifying, not deciding.

Do not write to a number either. When one corpus was stripped under a 450-word cap,
eleven of twenty-five records landed within five words of it, which is padding dressed as
compliance.

## What actually binds

Two mechanisms were tested on clean agents given only this standard and a real decision.

The citation limit binds. Both runs produced zero `file:line` citations in the Decision
Outcome block. It is countable and binary, so there is nothing to interpret.

The length ceiling does not bind. The same two runs produced 690 and 638 words against a
stated 456. A number given as a cap gets written to, and a number given as a ceiling gets
overrun.

So length is cut at review, not at writing time. When a record runs long, the excess is
almost always in the Context, and the cause is that the brief was treated as the record.
A brief hands over every fact that was gathered. A record carries only the forces that were
in tension when the decision was made. Cut in this order:

1. Facts that support a Consequence rather than the Context. They are already below.
2. The second and third example of something the first example established.
3. Any sentence describing what the code does now, rather than what was chosen.

## Template

```markdown
---
status: proposed | accepted | rejected | superseded by ADR-00NN
date: YYYY-MM-DD          # last update
decided: YYYY-MM-DD       # when accepted
deciders: ...
supersedes: ADR-00NN      # optional
---

# NNNN: <short noun phrase: the subject>

## Context and Problem Statement
<the forces in tension, value-neutral, with evidence: file:line references, spike results>

## Decision Drivers
* <force or constraint, including dev rulings>

## Considered Options
* <option 1>: <its trade-off, in this line>
* <option 2>: <its trade-off>   <!-- at least two real options; no straw men -->

## Decision Outcome
Chosen option: "<option>", because <justification tied to a driver>.
<What reversing this would cost.>

### Consequences
* Good, because ...
* Bad, because ...        <!-- required; include maintenance and operation, not only development -->
* Neutral, because ...

### Confirmation
<how a violation is detected: the test, CI gate, review rule, or "nothing checks this, because ...">

## More Information
<confidence; when to revisit; related ADRs; the code or docs holding the detailed design>
```

`Considered Options` carries each option with its trade-off on the same line. There is no
separate pros-and-cons section, because a template with both makes every record name its
options twice.

The Decision Outcome states the choice, the reason, and the cost of reversing it. Nothing
else. A good database record decides in two sentences: PostgreSQL is the better choice for
getting started quickly with minimal configuration, and changing it later means updating the
data-access configuration and rebuilding migrations.

Rules: one decision per record; numbers are sequential and never reused; rejected records
stay, with the reason; the `decided` date freezes while `date` moves with amendments.

## Anti-patterns (a review rejects on any hit)

- No alternatives, or a straw-man alternative.
- No "Bad, because" consequence.
- A justification that restates the decision ("we chose X because X does X").
- No Confirmation section, or one that names nothing checkable without saying why.
- Records a fact, a rename or a pin rather than a choice between options.
- Fails the significance test, so it belongs in `AGENTS.md`.
- Contains the detailed design (field tables, algorithms, more than one diagram), so move
  it to the code or the durable docs and link.
- More than one decision in one record.
- Only developer-facing consequences; the people who operate, maintain, or author content
  with the system go unmentioned.
- Marketing adjectives, or a numeric score for intangibles.
- An accepted record edited in place to change its decision.

## Titles

Name the subject, not a slogan: `Database`, `Request Authorization`, `Client State`. A
title like "Routing is the user's job" is the rhetorical cadence `plain-voice` bans,
showing up in the index.

## Writing

Open the Context on the concrete thing that forced the decision. An opener like "Enterprise
applications frequently require seamless, automatic redirection" tells a reader of the repo
nothing.

Show a code or payload sketch where it teaches faster than a paragraph.

An option nobody seriously considered is a straw man. Say so in a clause and move on.

Use the voice in `skill://plain-voice`: flat declaratives, no em-dashes, no rhetorical
cadence. A record is read years later by someone deciding whether to undo it.

## Changing an accepted record

Accepted records change only by supersession. Typos, link fixes, and removing specification
are the exceptions. The decision, its reasoning and its consequences are never edited.

Supersession is scoped, and the scope is written into the status: name the clause that is
replaced and the clauses that still stand, so a reader knows what survives without diffing
two records.

A supersession chain inside a young repository is an author amending himself, and it is
worth nothing to a reader. Resolve the chain and record the decision as it stands.

## Older records

Records that predate this standard stay as they are. New records follow it. Rewriting an
older corpus is its own task, done only when the dev asks.

Sources: Nygard, *Documenting Architecture Decisions* (2011); MADR 4.x (adr.github.io/madr);
Zimmermann, *How to create ADRs, and how not to* (2023).
