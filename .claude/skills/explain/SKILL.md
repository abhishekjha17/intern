---
name: explain
description: Explain code in plain language. Walks through logic step-by-step, identifies patterns, and describes data flow.
argument-hint: [file-or-function]
allowed-tools: Read, Grep, Glob
---

Explain the code specified by the user. Read the file or function indicated by `$ARGUMENTS`.

Structure your explanation as:

1. **Overview** — What this code does in one paragraph.
2. **Key Components** — Walk through the major pieces (functions, types, modules).
3. **Data Flow** — How data moves through the system.
4. **Notable Details** — Anything surprising, clever, or easy to miss.

Assume the reader is a junior-to-mid developer. Define jargon before using it. Explain the "why" behind decisions, not just the "what."
