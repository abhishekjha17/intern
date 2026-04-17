---
name: refactor
description: Analyze code for refactoring opportunities — code smells, structural improvements, better abstractions.
argument-hint: [file-or-path]
allowed-tools: Read, Grep, Glob
agent: refactor-advisor
---

Analyze the code at `$ARGUMENTS` for refactoring opportunities.

Identify code smells, suggest structural improvements, and propose concrete refactoring patterns. Each suggestion should be incremental and implementable in a single PR.
