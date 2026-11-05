# Architecture decision records

This log records the decisions that shape skua: the ones that would cost real rework, change
a published interface, or move a quality attribute if reversed. It is the "why". The code is
the record of how the system works, and the repo's durable docs hold usage. ADRs link there;
they never restate it.

An accepted record is immutable. A change of mind is a new record that supersedes the old
one, which stays in the tree. The full standard is the `adr` skill
(`.agents/skills/adr/SKILL.md`).

## Index

| ADR | Decision | Status |
| --- | --- | --- |
| [0001](0001-sandbox-provider.md) | Agent pods run on agent-sandbox, with one template and warm pool per profile and one claim per run | accepted |
| [0002](0002-llm-gateway.md) | Inference goes through LiteLLM with one virtual key per run; pods hold no provider keys | accepted |
| [0003](0003-agent-runner-injection.md) | An init container injects `skua-runner`, which wraps the agent as PID 1 over stdio | accepted |
| [0004](0004-control-plane-storage.md) | Results live in `AgentRun` status and git; the control plane uses no volumes | accepted |
| [0005](0005-github-credentials.md) | The operator mints one-hour single-repo tokens from the installer's existing GitHub App | accepted |
| [0006](0006-omp-relay-provider.md) | Remote runs appear in omp through an extension relay provider with a passthrough `skua_step` tool and one agent file per profile | accepted |
| [0007](0007-code-structure.md) | Go code is organized by component, then feature slice, with shared `contract` and `integration` groups; `test/layout` enforces the import rules | accepted |

## When to write one: the significance test

An ADR is warranted only if both gates pass. Otherwise it is a convention and belongs in
`AGENTS.md` or the durable docs.

1. **Category:** the decision affects structure, a non-functional characteristic, a
   dependency, a published interface, or a construction technique.
2. **At least two of:** high value or risk, such as data loss, lockout or credential
   exposure; a stakeholder's concern, such as the devs dispatching agents or the people
   operating the cluster; an unusual quality requirement; an external dependency outside the
   team's control; a cross-cutting effect on every feature; first of its kind in the repo;
   troublesome in the past.

Reversal gate: if undoing it is a rename, a flag flip, or a refactor with no user-visible
change, it is not an ADR. A deletion default that, once flipped, destroys data nobody can
get back passes this gate.

## Length and voice

The one hard limit is zero `file:line` citations in the Decision Outcome block, counting
Consequences and Confirmation. They go in `More Information` as a bare path, or nowhere.
This is what catches specification, because a record cannot describe implementation without
naming it.

Length has no cap, and shorter is better. The calibration is a human-written corpus of nine
records: 316 words on average, and the longest is 456. A record that runs past 456 words is
specifying, not deciding. Do not write to a number either: a cap gets padded up to.

- Open the Context on the concrete thing that forced the decision, not on the industry.
- Show a code or payload sketch where it teaches faster than a paragraph.
- An option nobody seriously considered is a straw man. Say so in a clause and move on.
- `Good, because` and `Bad, because` bullets carry the consequence. They do not restate the
  decision.
- Title the record with its subject, not a slogan: `Database`, `Request Authorization`,
  `Client State`.
- Write in the voice of `.agents/rules/plain-voice.md`: flat declaratives, no em-dashes, no
  rhetorical cadence. A record is read years later by someone deciding whether to undo it.

## Template

File name: `NNNN-<slug>.md`.

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

Rules: one decision per record; numbers are sequential and never reused; rejected records
stay, with the reason; the `decided` date freezes while `date` moves with amendments.

An accepted record changes only by supersession, with three exceptions: typos, link fixes,
and removing specification. The decision, its reasoning and its consequences are never
edited. Supersession is scoped: the status names the clause that is replaced and the clauses
that still stand.

## Anti-patterns (a review rejects on any hit)

- No alternatives, or a straw-man alternative.
- No "Bad, because" consequence.
- A justification that restates the decision ("we chose X because X does X").
- No Confirmation section, or one that names nothing checkable without saying why.
- Records a fact, a rename or a pin rather than a choice between options.
- Fails the significance test, so it belongs in `AGENTS.md`.
- Contains the detailed design (field tables, algorithms, more than one diagram), so move it
  to the code or the durable docs and link.
- More than one decision in one record.
- Only developer-facing consequences; the people who operate or maintain the system go
  unmentioned.
- Marketing adjectives, or a numeric score for intangibles.
- An accepted record edited in place to change its decision.

Sources: Nygard, *Documenting Architecture Decisions* (2011); MADR 4.x (adr.github.io/madr);
Zimmermann, *How to create ADRs, and how not to* (2023).
