<!--
The PR title must be a conventional commit: it becomes the squashed commit
subject on main and decides the next version.

  feat: add a thing      -> next minor      fix: correct a thing -> next patch
  feat!: remove a flag   -> next major      docs: explain a thing -> no release
-->

## What this changes

<!-- One or two sentences. What is different after this merges? -->

## Why

<!-- The problem this solves. What did you consider and reject? The diff shows
     what changed; it cannot show what you knew when you changed it. -->

## How it was verified

<!-- Which test fails without this change? What did you run by hand? -->

## Checklist

- [ ] The title is a conventional commit
- [ ] `make check` passes
- [ ] A test covers the change, and fails without it
- [ ] `docs/` is updated if behaviour changed
- [ ] Breaking changes are marked `!` and explained in the commit body
