---
name: write-docs
description: Generate documentation for code — godoc comments, package docs, usage examples.
argument-hint: [file-or-package]
allowed-tools: Read, Grep, Glob, Edit
---

Generate documentation for the code at `$ARGUMENTS`. Read the source first to understand the API.

## Guidelines

- Write godoc-style comments for Go (or equivalent for other languages).
- Package comments: `// Package X provides...`
- Function comments: start with the function name.
- Include usage examples that demonstrate the primary use case.
- Document parameters, return values, error conditions.
- Don't document the obvious — `// Close closes the connection` adds nothing.
- Include "gotchas" — things that surprise users or are easy to misuse.
- Keep examples runnable.

Apply the documentation directly to the source files using Edit.
