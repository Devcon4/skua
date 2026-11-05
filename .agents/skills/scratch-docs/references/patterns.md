# Scratch-doc templates

Skeletons are starting points, not forms to fill. Start from the matching one, then reshape
it to the problem: delete sections with nothing to say, add ones the problem demands, vary
counts. What never bends: the three-line header, and the evidence and disposition rules
where a template states them.

## `<slug>.proposal.md`

A concise pitch to the dev in plain English. Assume the dev has not read the code. Every
decision carries its reasoning and evidence; terms match the code, schemas, `AGENTS.md`
and README.

```markdown
Status: draft
Updated: <YYYY-MM-DD>
Owner: <dev or agent>

# Proposal: <title>

**Recommendation:** <two or three sentences: what to build and why.>

**Key tradeoff:** <the one cost the dev accepts by approving.>

**Needs your call:** Q1, Q2 <or "nothing">

## Pitch (unconfirmed)
<Only in unattended runs, until the dev confirms the pitch. One sentence, then Shape,
Assumed, New terms, Not doing.>

## Problem
<What hurts or what is wanted, in the dev's terms. Two to five sentences.>

## Constraints
- <stated constraint> (dev)
- <inferred constraint> (inferred)

## Current state
<Only what the dev needs to judge the design, grounded (`path/file.ts:123`), a screenful at
most. A fact that justifies one decision moves next to that decision; a fact no section uses
stays in the notes doc.>

## Proposed design
<The recommended path in enough detail to attack: data flow, touched components,
contracts. Code-shaped: a short sketch of the key signatures or types. Flow-shaped and
non-obvious: a small mermaid diagram of that flow.>
- Tradeoffs: <cost or risk>

### Alternatives considered
- <name>: <what it is; why it loses>. Promote to a subsection when two paths are close and
  the dev should pick. Zero alternatives is honest when one path is.

## Risks and migration
- <risk> → <mitigation>
- Rollout: <order>. Rollback: <how>.

## Execution sketch
<High-level phases only; delete if obvious. The spec does the decomposition.>

## Open questions
1. Q1: <a call only the dev can make>

## Decisions
- D-1 (dev, <YYYY-MM-DD>): <ruling, close to the dev's words> (answers Q<n>)

## Review log
<For agents, not the dev: what was reviewed, so a later session does not re-litigate it.>
- round 1 (<date>, <size>, mode: parallel|sequential): fact 3, risk 4, scope-add 2 (→ Q3,
  Q4), scope-cut 1, prose 2; accepted 9, rejected 1; net +12 lines.
```

## `<slug>.spec.md`

The proposal turned into a contract agents execute. `## Context` passes verbatim as the
task `context`; each unit section passes verbatim as one `task`. Anchor edits to symbols
and strings, with line numbers as hints only: lines drift between writing and execution.

```markdown
Status: draft
Updated: <YYYY-MM-DD>
Owner: <orchestrator>
Source: <slug>.proposal.md revised-N
Board: <board slug, or none>

# Spec: <title>

## Waiting on the dev
<Only without a board: questions blocking a unit, each with the unit id.>

## Context
Goal: <two lines>
Rules for implementers:
- Edit only the files your unit lists; create files only at the paths it gives.
- Check cheapest first: `lsp` diagnostics on every file you touched, then Acceptance
  checks that need no build. Run a build or test only when it is listed under
  Parallel-safe checks or your language has no LSP; report any other check as `deferred`
  with its exact command.
- Do not write this spec or the board.
- Never guess. A wrong Contract, a file outside your Files, a broken dependency or a dev
  call goes to the orchestrator: message it and keep working, or yield `Status: blocked`
  with the question and your options.
- A collision with another running unit goes to the orchestrator first. Only when it has
  not answered and waiting would waste work, message that unit by its roster name; agree
  on ordering or mechanics only, never on a Contract, Files or Acceptance change.
- Report: Status (done | partial | blocked), files changed, deviations from the Contract,
  each Acceptance item with its check kind (lsp | static | run | deferred) and output,
  Peer agreements (or none), open questions.
Terms and units: <invariants, coordinate systems, glossary>
Shared contracts: <types and signatures more than one unit uses>
LSP: <languages confirmed in preflight; fallback for any without a working LSP>
Parallel-safe checks: <commands implementers may run side by side, or none>
Build gate: <exact command and expected result; the orchestrator runs it once per wave>
Wave roster: <filled at spawn: S-1 → S1Name, S-2 → S2Name>
Proof: <where screenshots and command output go>

## S-1 <name>
Implements: <proposal section>, D-2
Depends on: none
Files: edit `<path>`; create `<path>`
Card: #<n> <only when the spec names a board>
Contract: <signatures, types, behaviour>
Acceptance:
- <observable check> → `<exact command>` prints <expected>
Out of scope: <what this unit must not do>
Verify: none | review: <why: hard to undo, security, migration, visual>
Status: todo
Evidence: <check output from the report and the wave gate, proof path>

## Waves
1. S-1, S-2 (no shared files). Gate: <build passes>.
2. S-3. Gate: <build passes>. Handoff: <what the dev looks at or wires by hand>.

## Coverage
| Proposal item or decision | Units |
| --- | --- |
| <section> | S-1, S-3 |
| D-2 | S-3 |
| <item> | dropped: <reason>, dev OK <date> |

## Amendments
- <date>: S-<n> <what changed> because <why>.
```

## `<slug>.investigation.md`

```markdown
Status: draft
Updated: <YYYY-MM-DD>
Owner: <agent>

# Investigation: <question>

Unblocks: <decision or work waiting on the answer>
Budget: <agents, time>. Stop when <rule>.

## Known
- <grounded fact> (`file:line`, output, screenshot)

## Branches
<mermaid tree when there are more than three branches>

| Id | Hypothesis or option | Probe | Cost | Result | Verdict |
| --- | --- | --- | --- | --- | --- |
| B1 | <cause or option> | read: <what> | low | <what was found> | pruned: <why> |
| B2 | <cause or option> | build: <spike> | medium | <what happened> | confirmed |
| B3 | <cause or option> | measure: <what> | low |  | open |

## Spike code
<path; removed on <date>, or kept as a stopgap with the dev's OK on <date> and removed by
<spec unit or card>.>

## Exit
Answer: <one or two sentences>.
Next: <proposal | spec | card #n | ADR | dead end recorded>.
```

## `<slug>.findings.md`

```markdown
Status: in-review
Updated: <YYYY-MM-DD>
Owner: <orchestrator>

# Findings: <title> (target: <slug>.proposal.md rev N)
Board: <size>; lenses: <list>. Mode: <parallel | sequential>

| # | Kind | Sev | Lens | Finding | Evidence | If ignored | Disposition |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | fact | major | fact check | <claim> | `<file:line>` | <consequence> | fixed in rev N+1 |
| 2 | scope-add | minor | author | <claim> | <quote> | <consequence> | → Q3 for the dev |
| 3 | risk | blocker | runtime | <claim> | `<file:line>` | <consequence> | rejected: <why> |

Kinds: fact, risk, scope-add, scope-cut, prose. Severity: blocker, major, minor.
Dispositions follow the skill's table; scope-add rows go to the dev as questions.
A reviewer reporting nothing lists what it checked.
Later rounds append `# Round N (target: rev …)` with their own reviewers. Numbering
continues across rounds, and each round after the first verifies the prior round's
`fixed` rows.
```

## `<slug>.notes.md`

```markdown
Status: draft
Updated: <YYYY-MM-DD>
Owner: <agent>

# Notes: <research question(s)>

## Q: <question>
A: <answer>. Sources: <file:line, URL, command output>.
```

## `<slug>.brainstorm.md`

The output of an expert room. Synthesis is single-author; group proposals are appended
verbatim as evidence, never rewritten. Large sessions branch out: a standalone group finding
graduates into its own doc rather than dying in an appendix.

```markdown
Status: draft
Updated: <YYYY-MM-DD>
Owner: dev + <orchestrator>
Session: <small|medium|large>: <one-line why>

# Brainstorm: <design question>

How this was made: <N> agents (<lens list>) in <M> groups assigned by design family; each
group's full proposal is appended verbatim. This header is the synthesis.

## Convergence and divergence

| | <Group A> | <Group B> | <Group C> |
| --- | --- | --- | --- |
| Shape | ... | ... | ... |
| <the dev's key constraint> | ... | ... | ... |
| Files touched / net LOC | ... | ... | ... |
| <hole it closes / risk it adds> | ... | ... | ... |
| Dissent recorded | ... | ... | ... |

## My read (yours to overrule)
<Two to four numbered positions: what to adopt, what to decide separately, what to archive.>

## Dev calls
1. Q1: <decision only the dev can make>

---

# Appendix: group proposal <Group A>
<verbatim group document>
```

## `<slug>.matrix.md`

Two-dimensional: rows and columns are the problem's axes. A list of work items with a status
column is a spec.

```markdown
Status: executing
Updated: <YYYY-MM-DD>
Owner: <orchestrator>

# Matrix: <title>
Axes: rows = <entity or workflow>; columns = <operation or coverage layer>.

|        | create | update | delete | drift-repair |
| ------ | ------ | ------ | ------ | ------------ |
| realm  | ✔ envtest `TestRealmCreate` | doing | todo | n/a: <why> |
| client | todo   | todo   | todo   | todo         |

Cells: todo, doing, blocked: <why>, ✔ <evidence>, n/a: <why>.
A ✔ needs evidence in the cell. An empty cell is an unexamined hole; resolve every cell
before `done`. Single writer: the orchestrator merges subagent cells.
```
