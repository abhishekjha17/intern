---
name: write-tests
description: Generate unit tests for the specified code. Produces table-driven tests covering happy path, edge cases, and error conditions.
argument-hint: [file-or-function]
allowed-tools: Read, Grep, Glob, Bash, Write, Edit
---

Generate unit tests for the code specified by `$ARGUMENTS`. Read the source file first to understand the functions and types.

Project test patterns for reference:
```!
find . -name '*_test.go' -not -path './vendor/*' | head -5
```

## Requirements

- Write table-driven tests following Go conventions (or equivalent for other languages).
- Cover: happy path, edge cases, error conditions, boundary values.
- Use descriptive test names: `TestFunctionName_Scenario`.
- Use the standard `testing` package — no external test frameworks unless the project already uses one.
- Each test should be independent — no shared mutable state.
- Keep test data minimal — simplest input that exercises the behavior.
- Include negative tests: invalid inputs, error conditions.

Write the test file and verify it compiles with `go test -run=XXX ./path/to/package`.
