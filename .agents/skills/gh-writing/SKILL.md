---
name: gh-writing
description: "Write GitHub-facing text: pull request bodies, issue bodies, commit messages, branch names. Use whenever running `gh pr create`, `gh pr edit`, `gh issue create`, `gh issue comment`, or writing or suggesting a commit message, and when deciding whether work becomes an issue, a PR, or neither. Supplies a fixed block toolbelt, a selection table, checkable voice rules, and filing rules."
argument-hint: "What is being written, e.g. 'PR body for the redirect watch fix' or 'issue for the flaky server spec'"
---

# GitHub-facing writing

Three documents, three readers. Write each for its own reader; never paste one into another.

| Document | Reader | Purpose |
| --- | --- | --- |
| Commit | an engineer in `git log` years from now, no browser | inform. The only copy guaranteed to survive |
| PR body | a reviewer this week, two minutes | get approved, or surface the defect |
| Issue | a maintainer triaging, no diff yet | get scheduled |

The reader has **not** read the conversation that produced the work. Assume nothing from it.

## 1. Blocks

Six PR skeletons. Headings are literal. Never rename them, never invent one.

```markdown
<Lede. One sentence, no heading, <=200 chars. What changed and its measured effect.>

Fixes #N.                          <- only when a tracked issue exists

## Change
- <what changed, present tense, one bullet per kind of change>

## Why
<Root cause as fact, carrying its evidence: file:line, output, or a number.>

## Verification
- <check>: <result with number or command output>
- N/A: <reason>                   <- when nothing runs, e.g. docs-only

## Risk
<What could break, the rollback, and any action an existing deployment must take.>
```

Issues take one of three shapes. Pick one, never two.

```markdown
## Observed / ## Expected / ## Evidence        <- something is broken
## Problem / ## Proposal / ## Acceptance       <- something is missing
## Question / ## Finding / ## Decision         <- something must be decided
```

`## Decision` reads `open` while pending. The third shape covers a spike that concludes
"do nothing", a decision worth recording, and a question aimed at the dev.

Anything fitting no block is evidence (put it in `Verification`) or narration (cut it). A
cut idea worth keeping becomes an issue.

## 2. Selection

| Change type | Blocks, in order |
| --- | --- |
| Bug fix | Lede, Change, Why, Verification |
| Feature | Lede, Change, Why, Verification |
| Removal or breaking change | Lede, Change, Why, Verification, Risk |
| Revert | Lede, Change, Why (what regressed), Verification |
| Chore, CI, dependency bump | Lede, Change, Verification |

`Fixes #N.` is independent of the row: present when a tracked issue exists, absent
otherwise. Most PRs have none.

Append `## Risk` when the diff touches deployment manifests or charts, an RBAC or
permission rule, a delete or revoke path, a migration or a save-data format, or a file
matching `**/secret*`, `**/*auth*`, `**/*credential*`.

Classify by the diff: adds an exported symbol, flag, config or schema field → feature; removes one →
removal; restores a previous commit → revert; touches only `.github/`, `Makefile`, CI
config or dependency manifests → chore.

## 3. Voice rules

1. No first person. Zero matches for `\bI\b|\bwe\b|\bmy\b|\bour\b` in the prose (code
   blocks and quoted output excluded), allowing `I/O`.
2. No residue or theatre. Zero matches for: `as discussed`, `the dev calls this`, `I found`,
   `turns out`, `bit me in`, `we decided`, `very`, `really`, `just`, `simply`, `basically`,
   `actually`, `clearly`, `deliberately`. No rhetorical questions. No bolded prose. Bold is
   for table cells.
3. Every reference resolves inline. Any `#N`, ADR, or house nickname carries its load-bearing
   fact in the same sentence. `Related: #25` is not a sentence; it is a task handed to the
   reader. "this estate" fails; name the referent.
4. Fixed headings only, from the lists above.
5. No em-dash. Not a punctuation preference: an em-dash in agent output is a reliable tell
   that the sentence drifted into essay register, so treat one as a signal to rewrite the
   whole sentence, not to swap the character. An em-dash almost always joins two clauses
   that should be two sentences, or fences an aside that should be cut. Same for the en-dash
   and for ` - ` used as a dash.
6. No secret material. A `kubectl get secret` or `describe secret` output block is banned
   outright. Cite the Secret by name and key instead. Any line matching
   `(?i)(password|token|secret|apikey|authorization|bearer)\s*[:=]` has its value replaced
   with `<redacted>`, key preserved. Redact before truncating, never after.

### Plain language

Rhythm, not vocabulary. A sentence shaped for emphasis, contrast, or timing is doing
something other than informing. The full list of beats and a rewrite for each is in
`skill://plain-voice`, which applies to everything a human reads, not only GitHub text.

The short version: one flat declarative sentence per fact. No setup, no turn, no landing.
Cut em-dashes, verdict sentences, `that is the real problem`, `which is the sharper
framing`, three-item escalations, and openers like `Here is the thing`.

Two tests: delete the clause and reread, and if no fact is lost it was cadence; read it
aloud, and if the rhythm is noticeable, flatten it.

Identifiers are not secrets. Hostnames, realm names, client IDs, namespaces and versions
stay. They are the evidence most PRs exist to report. The line is credential material:
never pair an identifier with a live token for that same host.

Length: shorter is better, and there is no number. Write what the change needs, then cut
what the reader does not. A three-file fix that runs to four paragraphs is explaining
itself. A removal touching 25 files with a migration note is long because the change is.

The test is not a character count. It is whether each sentence carries a fact the reader
needs. Fluff and exposition are the target, not length.

Evidence is never what to cut. Numbers, `file:line`, and command output stay. Cap a pasted
block at roughly 10 lines and truncate with an ellipsis rather than pasting a log.

Deliberate deviation from a rule is marked `<!-- gh-writing-exception: <rule>, <reason> -->`.

## 4. When to file

| Situation | Action |
| --- | --- |
| Defect in a file already open for today's task, and fixing it does not expand today's diff | Fix it now. No issue. This is the default |
| Defect needs its own root cause, its own verification, or files outside today's task | File an issue, continue today's task |
| The dev must choose between options before code can start | File the issue first, options under `## Proposals` |
| A dev remark in conversation | File only if it has an observed-versus-expected gap AND survives without the chat context. Otherwise it belongs in `docs/scratch/` |
| Duplicate, speculative, or declined by the dev | Do not file |
| A live, unmitigated exploit path | Tell the dev directly. Do not describe it in an issue until it is fixed |

One issue per independently provable root cause, not per remediation. An audit finding
seven candidates is one issue listing seven, narrowed by comment before implementation.

A PR needs no issue by default.

## 5. Branches and commits

Branch: `<type>/(<issue>-)?<slug>`, type in `{feat, fix, chore, ci, docs}`, slug kebab-case,
at most five words.

```
^(feat|fix|chore|ci|docs)/(\d+-)?[a-z0-9.]+(-[a-z0-9.]+){0,4}$
```

Commits are grouped logically and atomically; a PR often has several, because commit
boundaries are what make a large diff reviewable. When squashing at merge, replace GitHub's
default body. It concatenates commit subjects into `* <subject>` bullets and that becomes
the permanent record. Subject: imperative, sentence case, no
trailing period, no type prefix, <=65 characters. Body: third person, prior state stated as
fact, evidence inline. No Conventional Commits unless the repo's tooling parses a type
prefix, and its `AGENTS.md` says so.

A repo whose versioning reads a commit trailer (GitVersion's `+semver: major|minor`)
documents the trailer in its `AGENTS.md`. Nothing else in a commit is parsed.

The PR title is the commit subject; squash-merge appends `(#N)` verbatim.

## Worked example

Before, PR #32 as originally written (2595 characters, opens with a rhetorical question,
first person, bolded reveals, and a section answering a question the reviewer never asked):

> Answers "why is the Tests cache 690MB when the vuln one is 40MB" — and turned up a bug in
> the cache config I wrote in #28. [...]

After:

```markdown
A Go module cache path that has never existed forced every CI job to re-download every
module; caching the correct path cuts the Tests job from 2m38s to 2m03s.

## Change
- Cache `/go/pkg/mod/cache/download` instead of `~/go/pkg/mod`, which resolves to no path
  in the `golang:1.26` image (`GOPATH=/go`).
- Drop `~/.local/share/kubebuilder-envtest`; never populated, assets land in `bin/k8s`.

## Why
`~/go/pkg/mod` has never existed under `GOPATH=/go`. Every job re-downloaded every module,
including runs the workflow reported as cache hits: 95 `go: downloading` lines, 32s of the
Tests step.

## Verification
| Metric | Before | After |
|---|---|---|
| `go: downloading` lines | 95 | 0 |
| Tests job | 2m38s | 2m03s |
| Cache payload | 552MB | 121MB |

## Risk
Editing the cache `path:` list invalidates existing entries once; the first run on `main`
after merge pays a cold build.
```

Prose runs ~680 characters. Every measured number survives. What went: the rhetorical
question, `I wrote in #28`, the bolded reveals, and the 690MB-versus-40MB tangent.
