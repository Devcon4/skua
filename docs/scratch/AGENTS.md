# docs/scratch rules

Short-lived working documents: proposals, specs, investigations, findings, research notes,
brainstorms and coverage matrices. Only proposals (`*.proposal.md`) and this file are
committed; every other scratch doc is gitignored and stays local. Each is disposable once its
outcome is promoted. The playbook, workflows and
templates live in the `scratch-docs` skill (`.agents/skills/scratch-docs/`).

Inviolable rules:

1. **Name.** `docs/scratch/<slug>.<type>.md`, one topic per file, with type one of
   `proposal | spec | investigation | findings | notes | brainstorm | matrix`. Images and
   mockups a doc needs sit beside it.
2. **Header first.** The first three lines are `Status:`, `Updated:` (date) and `Owner:`
   (dev or agent name). `Status:` holds exactly one of `draft | in-review | revised-N |
   approved | executing | done`, with no other text on the line. Trust the header over the
   body when a doc looks stale, and confirm a claim against the repo before acting on it.
3. **Never cited.** No committed code, durable doc or test references a `docs/scratch/`
   path. Kanban cards may. The dev decides what is committed or deleted; agents never
   `git add`.
4. **Single writer.** One agent updates a doc at a time. Subagents return rows or sections
   for the orchestrator to merge, unless the doc assigns per-agent rows. During a spec run
   the orchestrator is the only writer of the spec.
5. **Evidence or it did not happen.** A `done`, `PASS` or `fixed` mark needs its evidence
   filled in: command output, a test run, or `file:line`.
6. **No secrets.** Name Kubernetes Secrets by name and key, LLM gateway keys, GitHub tokens
   and OIDC clients by name. Never paste a credential, token or key value.
7. **Promote, then delete.** Decisions that change behaviour become ADRs in `docs/adr/`.
   Conventions go to the relevant `AGENTS.md`. Usage goes to `README.md`. When every doc for
   a slug is `done` and its promotions have landed, the doc can be deleted locally; for a
   committed proposal, hand the dev a one-line `git rm`. A scratch doc that outlived its work
   is a defect.

Templates: `.agents/skills/scratch-docs/references/patterns.md`.
