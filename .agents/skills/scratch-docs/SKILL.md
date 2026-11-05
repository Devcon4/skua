---
name: scratch-docs
description: "Create and work short-lived planning documents under docs/scratch/: proposals, specs that agents execute, investigations of open questions or bugs, review-board findings, research notes, brainstorms and coverage matrices. Use when a dev lays out a change to design, when an approved proposal needs a spec to execute, when a question or bug has several possible answers to probe, or when design work should survive iteration rounds with the dev. Includes the draft, pitch check, review board, revise, spec and execute workflow."
argument-hint: "What to draft, e.g. 'proposal for the save-file migration' or 'spec for the approved snow proposal'"
---

# Scratch documents

Scratch docs are short-lived working artifacts in `docs/scratch/`. Design work, agent
execution and iteration with the dev happen in a file instead of chat scrollback, and the
file is deleted once its outcome is promoted. `docs/scratch/AGENTS.md` restates the
inviolable rules for agents that land there without this skill. Where a repo's
`docs/scratch/AGENTS.md` adds a type or overrides a rule, the repo file wins for that repo.

## Lifecycle rules

- **Name:** `docs/scratch/<slug>.<type>.md`, one topic per file. Types: `proposal`,
  `spec`, `investigation`, `findings`, `notes`, `brainstorm`, `matrix`. Images and mockups
  a doc needs sit beside it.
- **Header:** the first three lines are `Status:`, `Updated:` (date) and `Owner:` (dev or
  agent name). `Status:` holds exactly one of `draft | in-review | revised-N | approved |
  executing | done`. Context belongs in the body, never on the status line.
- **Never cited:** committed code and durable docs never reference a scratch path. Kanban
  cards may, because a board is working state too. The dev decides what is committed or
  deleted; agents never `git add`.
- **Promote, then delete:** a decision that changes behaviour becomes an ADR under
  `docs/adr/`. Usage, design intent and measured behaviour go to the repo's durable docs,
  which its `AGENTS.md` names. Conventions go to the relevant `AGENTS.md`. When every doc
  for a slug is `done` and its promotions have landed, hand the dev a one-line `git rm` for
  those files. A scratch doc that outlives its work is a defect.
- **Stale docs:** trust `Status:` and `Updated:` over the body, and confirm a claim against
  the repo before acting on it.
- **Digest:** omp's `scratch-digest` extension lists the repo's unfinished scratch docs,
  done docs still on disk, and docs without a header at session start. Use it to pick up
  where work stopped.

## Pattern playbook

Templates are in [patterns.md](./references/patterns.md). They are starting points: keep
the header and the evidence and disposition rules, then reshape sections to the problem.

| Type | Use when | Key properties |
| --- | --- | --- |
| `proposal` | the dev describes a feature or architecture change to design | A plain-English pitch with one recommended path. The first screen is the dev page: recommendation, key tradeoff, calls needed. Holds the dev's requirements as `(dev)` constraints and `D-` decisions. Decomposition waits for a `spec`. |
| `spec` | an approved proposal is ready to execute | The proposal turned into a contract for agents: a `## Context` every implementer gets, one section per unit with Files, Contract and Acceptance, waves, coverage back to the proposal, amendments. Unit status and evidence live here. Sections are written to pass verbatim as task `context` and `task`. |
| `investigation` | an open question or a bug has several possible answers | A tree of branches: each hypothesis or option, its probe (read, measure or build), result and verdict. Pruned branches stay recorded. A spike is a branch whose probe is building something. Ends with an exit that routes the answer. |
| `findings` | aggregating review board output | One row per finding: kind, severity, evidence, consequence if ignored, mandatory disposition. |
| `notes` | research fan-out results while a proposal is drafted | Per-question answers with sources. Merged into the proposal, then deleted. |
| `brainstorm` | the design space is open and the dev wants competing designs | Expert room output: single-author synthesis header plus every group proposal verbatim. |
| `matrix` | coverage is a cross-product of two dimensions | Rows and columns are the problem's axes, cells hold status and evidence, an empty cell is a hole. A one-dimensional list of work is a spec. |

## The proposal workflow

1. **Capture the ask.** Write `Problem` and `Constraints` from the dev's words before
   touching code. Mark each constraint `(dev)` or `(inferred)`.
2. **Research.** Ground every current-state claim in the repo (`file:line`). When the
   surface is broad, fan out one investigator per subsystem or question into a `notes` doc.
   Research is input: the proposal keeps only the facts the pitch turns on, each next to the
   decision it supports.
3. **Draft** in the proposal voice below, committed to one recommendation.
4. **Pitch check.** Before any review board, send the dev one sentence plus up to four
   bullets:
   - *Shape:* the one decision everything else hangs on.
   - *Assumed:* the `(inferred)` constraints.
   - *New terms:* names the draft introduces.
   - *Not doing:* scope left out.

   In an interactive session, stop and wait; corrections become `D-` entries. In an
   unattended run (the dev is away: an overnight goal, or "execute when done" before
   leaving), write the pitch at the top of the doc under `## Pitch (unconfirmed)` and
   continue, so it is the first thing the dev reads.
5. **Review board** under the contract below. Aggregate into a `findings` doc and
   disposition every finding.
6. **Revise.** Bump `Status: revised-1` and append to `## Review log`.
7. **Present.** The message mirrors the dev page: the recommendation in two or three
   sentences, the key tradeoff, what the board changed, and the numbered questions.
8. **Iterate.** Each answer becomes a `D-` entry. Re-run only the lens the change affects,
   plus a fix-verification pass: a `fixed` disposition is a claim until checked against the
   revised doc. Bump `revised-N` and append to the review log.
9. **Approve and execute.** On approval set `Status: approved`, write the spec and execute
   it. "Execute when done" approves the draft plus the board's fixes. Scope-add questions
   stay open in the proposal, and on a card for the dev when there is a board; they never
   block execution.

### Decisions and questions

Open questions are numbered `Q1, Q2, …` under `## Open questions`, each a call only the dev
can make. An answer moves to `## Decisions` as `D-n (dev, YYYY-MM-DD): <ruling, close to
the dev's words> (answers Qn)`, and the question is removed. `(dev)` constraints and `D-`
entries are the requirements: reviewers may flag them, only the dev changes them, and the
spec's coverage must trace to every one.

### The review board

The board finds what the draft got wrong. It does not add detail. A board that accepts
nearly every finding grows the design each round, so the contract below separates facts,
risks and scope.

**Size.** Pick the size, then the lenses that fit the proposal's risks and consumers:

| Size | Fits | Reviewers |
| --- | --- | --- |
| small | one subsystem, well bounded | fact check, one user lens, pitch editor |
| medium | several subsystems or a new concept | add a second user lens and the deletion lens |
| large | cross-domain or hard to reverse | add the pre-mortem |

**Roles.**
- *Fact check:* verifies every claim about current code (`file:line`) and about an API or
  tool against its source.
- *User lenses* use the design instead of critiquing it. The implementer tries to build
  from the doc and lists every place it would have to guess. The author, built from the
  `(dev)` constraints and `D-` entries, walks the authoring workflow in the real tools. The
  runtime walker traces concrete scenarios: save and load, a day boundary, an empty state,
  the slowest device.
- *Deletion:* lists what can be cut and what each cut saves.
- *Pre-mortem:* it is three months later and the design was ripped out; give the two
  likeliest reasons.
- *Pitch editor:* attacks the document, not the design. Does it read as a plain-English
  pitch, does its structure fit the problem, do terms match the code, is research sitting
  where no decision uses it, does any paragraph need the chat to parse.

**Brief.** Each reviewer gets the problem, the constraints, the `## Decisions` log, the
pitch's *Not doing* list and the draft's path. Never give a reviewer a checklist built from
the draft's own parts: it can then only check what the author already thought of.

**Reviewer contract.** Paste this into every reviewer prompt:

> Return at most 5 findings, most severe first. Zero findings is a valid answer if you list
> what you checked. Each finding gives: kind (`fact`, `risk`, `scope-add`, `scope-cut` or
> `prose`), severity (`blocker`, `major` or `minor`), the claim or section, evidence
> (`file:line`, quote or source), what happens if it is ignored, and the smallest fix.
> `(dev)` constraints and `D-` entries are fixed: report a conflict with one as a question
> for the dev, never as a change. Refute the draft; do not summarize it.

**Dispositions.**

| Kind | Rule |
| --- | --- |
| `fact` | Fix it. |
| `risk` | Fix it, or reject it with the reason the consequence does not apply. |
| `scope-add` | The orchestrator never accepts one. It becomes a `Q` for the dev unless a `(dev)` constraint or `D-` entry already requires it. |
| `scope-cut` | Accept it unless it removes something a `(dev)` constraint or `D-` entry requires. |
| `prose` | Apply the pitch editor's fix. |

**Rounds.** At most two rounds before the dev sees the draft. The review log records, per
round, the findings by kind, accepted and rejected counts, and the proposal's net line
change. If the net line count grows for two rounds without new facts, stop and present.

### Proposal voice

A proposal is a pitch that convinces the dev this is the right path, in plain English.
Assume the dev has not read the code you read:

- **Dev page first.** The first screen is the recommendation, the key tradeoff and the
  calls needed. Review log, findings and research sit below or in their own docs.
- **Explain, don't allude.** Every decision states its reasoning and the facts it rests
  on, with evidence inline (`file:line`, command output, a measurement), so the dev can
  check it.
- **Speak the domain's language.** Terms mean what they mean in the code, schemas,
  `AGENTS.md` and README. Never coin a synonym for an existing concept. A new concept is
  named once, defined in one line at first use, and used consistently after that.
- **Show the shape.** When the design is code-shaped, sketch the key signatures or
  contracts in a few lines the dev can mark up. Never write the implementation.
- **Concise.** Bullets and tables over paragraphs, short declarative sentences, no
  em-dashes. Say each thing once.

## Executing a spec

A spec turns an approved proposal into units an agent can execute without the chat:

1. `## Context`: the goal, rules for implementers (which files they may touch, how they
   check their work, when they ask), terms and units, contracts shared by several units,
   the LSP preflight result, the checks safe to run in parallel, the build gate, the wave
   roster, and where proof goes. It is passed verbatim as the task `context`, so it stands
   alone.
2. One `## S-<n> <name>` section per unit: Implements (proposal sections and `D-` ids),
   Depends on, Files, Card (when the spec names a board), Contract, Acceptance (observable
   checks with exact commands), Out of scope, Verify, Status, Evidence. Each section passes
   verbatim as one `task`. Ids are never reused.
3. `## Waves`: units with satisfied dependencies and no shared files run together. Each
   wave ends in a gate (the build passes) and, when the dev must look at or wire something
   by hand, a handoff line. Review, commits and playtest sign-off belong to the dev and are
   never units.
4. `## Coverage`: every proposal item and `D-` entry maps to units, or is marked dropped
   with the dev's OK. This makes "the plan is the commitment" checkable.
5. `## Amendments`: date, unit, change, reason.

**Spec review** is light because the design was already reviewed: check coverage, check that
no wave puts two units on one file, and run one implementer dry run that lists every place
it would have to guess. The spec needs no separate dev approval unless coverage drops an
item.

**Execution.** The orchestrator runs the spec itself with plain `task` subagents, one
implementer per unit. There is no lead or verifier role.
- **LSP preflight.** Before the first wave, open one real file per language in the spec's
  Files through `lsp`: diagnostics must come back and a go-to-definition must resolve. Fix
  the project or `lsp.json` when either fails. A language that still has no working LSP is
  recorded on the Context's `LSP:` line, and its units fall back to build checks.
- **Spawn.** All units of a wave go in one `task` call. Name each task (`S1Parser`) and
  write the wave roster (unit to name) into the Context. The `context` is the spec's
  `## Context` verbatim; each `task` is the unit section verbatim.
- **Implementers check their own unit**, cheapest check first:
  1. `lsp` diagnostics on every file they touched, with no new errors or warnings.
  2. Acceptance checks that need no build: reads, searches, static checks.
  3. A build or test only when the Context lists it under `Parallel-safe checks`, or when
     the language has no working LSP. Any other runtime check is reported as `deferred`
     with its exact command.

  Parallel builds contend for shared output folders and single-instance tools; LSP
  diagnostics are per file and safe to run side by side.
- **Report.** Status (`done | partial | blocked`), files changed, deviations from the
  Contract, each Acceptance item with its check kind (`lsp`, `static`, `run` or
  `deferred`) and output, peer agreements, open questions.
- **Wave gate.** Once every implementer in the wave has reported, the orchestrator runs the
  build gate and every deferred check once, with nothing else building. It reads the diff
  against each unit's Files and Contract, spot-checks the claimed evidence, then sets the
  unit's `Status:` (`todo | doing | done | blocked: <why>`) and `Evidence:`. A failure goes
  back to its implementer by message, which resumes it with its context. After a second
  failure the orchestrator fixes the unit itself or marks it blocked.
- **Independent review** only for a unit marked `Verify: review` (hard to undo, security,
  data migration, or acceptance a person must look at): after the wave gate, a read-only
  `reviewer` agent checks it against its Contract and Acceptance. The default is
  `Verify: none`.
- During the run the orchestrator is the only writer of the spec and the board.

**Ask, don't guess.** An implementer never settles these by guessing, by editing outside
its Files, or by changing the Contract: the Contract contradicts the code or the spec, the
work needs a file outside Files, the Contract leaves open behaviour a caller or the dev
would see, a unit it depends on is missing or broken, or the work needs a dev call. When
the question blocks part of the unit, it messages the orchestrator (`write agent://<parent>`)
with the options it sees and the one it would pick, and keeps working. When it blocks
everything left, it yields `Status: blocked` with the question; the orchestrator answers by
message, which resumes it. A question that needs the dev goes to the dev.

**Collisions between running implementers.** Waves never put two units on one file, so a
collision means the spec missed something: a shared generated file, a single-instance tool,
an interface that has not landed yet.
1. The implementer asks the orchestrator first and keeps working on what the collision does
   not touch.
2. Last resort, when the orchestrator has not answered and waiting would waste work, or the
   clash is purely between two running peers: message the peer directly by its roster name
   (`write agent://<name>`).
   - Peers may agree on ordering, turns on a tool or build output, who makes a shared
     mechanical edit, and an interface exactly as the spec states it.
   - Peers never change a Contract, Files, Acceptance, or anything traced to a `(dev)`
     constraint or `D-` entry. Those go to the orchestrator or the dev.
   - Both report the agreement under `Peer agreements:`. The orchestrator checks it at the
     wave gate and records it in `## Amendments` when it touches the spec.

**Where status lives.** The spec tracks units. A kanban board, when the repo has one, tracks
what the dev looks at: one card per deliverable the dev can see or playtest, plus cards for
the dev's own work such as art or decisions. Never one card per unit, and never a copy of
spec content: the card body names the spec units it covers. The orchestrator moves a card
when its first unit starts and when its last unit passes, attaches deliverable-level proof,
and writes a dev question as the card's `blocked` reason so it appears in the session
digest.
Without a board, dev questions go at the top of the spec under `## Waiting on the dev`.

## The investigation workflow

For an open question or a bug with several possible causes:

1. Write the question, what it unblocks, and a budget with a stop rule (agents, time, or
   "stop when one branch is confirmed").
2. Record what is known, grounded in the repo.
3. Lay out the branches: each hypothesis or option, its probe (`read`, `measure` or
   `build`) and its cost. Draw the tree in mermaid when it has more than three branches.
4. Probe independent branches in parallel, one subagent per branch. Record each result and
   a verdict: `open`, `confirmed`, `pruned: <why>` or `parked`.
5. A `build` probe is a spike. Record where its code lives and whether it is removed or
   kept. Keeping spike code as a stopgap needs the dev's OK, because a stand-in's shape
   leaks into the real design.
6. Exit with the answer and where it goes: a proposal, a spec for a small fix, a kanban
   card, an ADR, or a recorded dead end. Before the doc is deleted, each pruned branch a
   later session could plausibly retry moves to the durable home as one line: the
   subsystem's guide, or the gotchas in the repo's `AGENTS.md`.

A brainstorm compares competing complete designs; an investigation prunes hypotheses with
evidence. An investigation that finds the design space wide open exits into a brainstorm.

## The brainstorm room

When the dev asks for alternatives rather than a pitch, convene an expert room and let
groups compete:

1. **Size the session first**, before any spawn, and record it in the doc header
   (`Session: small|medium|large: <one-line why>`).

   | Size | Fits | Room | Output |
   | --- | --- | --- | --- |
   | small | one file or subsystem, a well-bounded question | 4 to 6 personas, one group | one brainstorm doc |
   | medium | a broad problem inside one domain | about 8 to 12 personas, 2 to 3 groups | one brainstorm doc |
   | large | cross-domain, the hardest problem class | 14 or more personas, 3 to 5 groups | brainstorm doc plus branch-out: group findings that stand alone graduate into their own docs |

   Oversizing burns agents and drowns signal; undersizing misses a design family. When torn,
   size down and let the room's "what's missing" justify a bigger re-run.
2. **Personas.** Each gets a distinct expertise and a one-line design instinct it must
   represent even when inconvenient. Keep one contrarian per likely design family. Reserve
   about a third of the seats for non-experts the experts must explain themselves to:
   - *juniors*, whose honest confusion is data;
   - *non-programmers* chosen per problem: the on-call operator, the support agent, the
     auditor, the tech writer, the end user;
   - *wildcards*: a beginner's-mind generalist, or an outside-field thinker when the problem
     has that shape.
3. **Shared brief** in a `local://` file every prompt references: the problem, the exact
   code to beat, hard constraints including the dev's rulings verbatim, the group roster,
   and the group deliverable contract. Agents share no chat history.
4. **Assigned groups.** The orchestrator forms the groups before spawning, around the
   design families research surfaced. Each group argues one family and holds at least one
   non-expert and one contrarian. Each persona's prompt names its group, its lead and its
   members' ids. A small session is one group.
5. **Messaging.** IRC offers DMs and `agent://all`, and `agent://all` reaches every agent in
   the session; there are no group channels. Members DM each other, leads trade ideas lead
   to lead by DM, and the only broadcast is each lead's `DONE <group>`.
6. **Group deliverable** to `local://<slug>-group-<name>.md` under a fixed contract: Thesis,
   Design (real code sketch), Deleted and added, Constraint audit, Proof against the harness
   realities, Migration cost, Risks, Dissent (required when any member disagrees). Any member
   may send the `DONE` if the lead dies; check that the file exists rather than trusting job
   status.
7. **Synthesis is single-author.** The orchestrator alone writes the scratch doc:
   convergence and divergence matrix, a read the dev can overrule, explicit dev calls, and
   the group docs appended verbatim as evidence. Fan out for ideas, never for the final text.

## Working with subagents (harness portability)

A repo may be worked from several agent harnesses. The concepts above assume parallel
subagents; degrade gracefully:

- **Fan-out** means several independent subagents running at once, each with a
  self-contained prompt; in omp this is the task tool. Without it, run the passes one at a
  time, ideally each in a fresh session, and record `Mode: sequential` in the review log or
  the spec's amendments.
- **Execution needs no custom agents.** Implementers are plain subagents and the spec's
  Context carries their rules. Without peer messaging, an implementer yields `blocked`
  instead of messaging, and the orchestrator settles collisions at the wave gate. Without
  LSP, the Context's `LSP:` line says so and implementers fall back to parallel-safe checks.
- **Independent review** uses a read-only reviewer agent (omp's `reviewer`); without one,
  run a fresh-context pass with no edit tools.
- **Prompts are self-contained.** A subagent gets the doc path, its question or lens, the
  repo areas to read, and the output shape. Reviewer prompts carry the reviewer contract
  verbatim. Never assume a subagent saw the chat.
- **The doc is the shared state.** Subagents return rows or sections; the orchestrator
  merges them.

## Rules

- Scratch docs never contain secrets or credentials. Name secrets by key and accounts by
  username, never by value.
- One writer per doc at a time, unless the doc assigns per-agent sections.
- Don't gold-plate. A ten-line task needs no proposal, and a one-file fix needs no spec.
  Reach for this skill when the work has real design surface, several execution steps, or
  several participants.
