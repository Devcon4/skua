---
status: accepted
date: 2026-11-04
decided: 2026-11-04
deciders: dev (proposal approval), orchestrator
---

# 0007: Code structure

## Context and Problem Statement
`internal/runner/runner.go` had grown to 792 lines holding nine concerns, and
`internal/operator` held four controllers in one package. Fakes followed four conventions in
three homes, and app packages booted API servers in their own tests. The `hack` directory
held 1,258 lines of shell that parsed YAML with `sed`. No check enforced any placement rule.

## Decision Drivers
* Dev ruling: component first, then feature slices, with a small shared domain on top.
* Dev ruling: one binary and one image.
* Dev ruling: envtest tests are black-box suites outside the app folders.
* Dev ruling: maintained tools where one exists, Go for the rest.
* The move changes no module the binary links.

## Considered Options
* Component first, then a package per feature slice, with `contract` and `integration`
  groups and a Go test holding the import rules: paths name the component and feature; about
  120 files move, and slices reach each other only through interfaces the root wires.
* One package per component, splitting only the large files: least churn; packages keep
  growing and tests keep reading private state.
* Layer first (`controllers/`, `handlers/`, `clients/`), as kubebuilder scaffolds: familiar
  to operator authors; one feature change spans several top-level folders.
* A module or binary per component was excluded by the one-binary ruling.

## Decision Outcome
Chosen option: "component first, then feature slices", because it is the only option where
a path names the component and feature it serves, which is the structure the dev asked for.

```
internal/contract/      types and names two components agree on
internal/integration/   clients of one external system
internal/<component>/   root wires slices; slices import contract, integration, support
test/integration/       every envtest suite, black-box
test/fakes/<system>/    process fakes; in-process fakes are <pkg>/<pkg>test
```

The `hack` shell scripts are replaced by Go under `tools/` and `test/`. mise pins the tool
binaries and Make stays the entry point. Reversing this moves the files back, drops the
layout test, and rewrites the kind environment and install checks as scripts.

### Consequences
* Good, because a reviewer finds a feature's code, tests and fakes from its name.
* Good, because the kind environment and install checks are type-checked Go.
* Bad, because history across the move needs `git log --follow`.
* Bad, because contributors install mise to get the pinned tools.
* Neutral, because the binary, image, CRDs and RBAC are unchanged, so operators see nothing.

### Confirmation
`test/layout` runs in `make test` and fails on an import or fake name that breaks the rules
in `AGENTS.md`. The `code-review` skill's Structure lens covers what it cannot see: long
files, mixed concerns, duplicated contract strings and new scripts.

## More Information
Rules 1 to 8 are in `AGENTS.md` under Code conventions. Revisit if a second binary is split
out.
