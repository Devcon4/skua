---
description: "GitHub text follows the gh-writing skill; read it before writing a PR, issue, commit or branch"
alwaysApply: true
---
# GitHub-facing writing

Before writing a PR body, an issue body, a commit message, or a branch name, read
`skill://gh-writing`. It holds the block skeletons, which blocks each change type uses,
and the rules for when work becomes an issue at all.

Three documents, three readers. A commit informs an engineer reading `git log` years from
now with no browser open. A PR body gets a reviewer to approve this week. An issue gets a
maintainer to schedule. Never paste one into another.

The reader has not read the conversation that produced the work. A sentence that only
makes sense to someone who was in it does not belong in the document. Any `#N`, ADR, or
internal nickname carries its load-bearing fact in the same sentence.

Shorter is better, with no fixed limit. Cut exposition, never evidence.

Bodies go in a file, so `gh pr create --body-file` rather than an inline `--body` or a
heredoc.

Writing style for these documents, and for everything else a human reads, is
`rule://plain-voice`.
