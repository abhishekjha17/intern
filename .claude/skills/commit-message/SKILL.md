---
name: commit-message
description: Generate a conventional commit message from the current staged or unstaged changes.
allowed-tools: Bash
---

Analyze the current git diff and write a commit message following the Conventional Commits specification.

Current changes:
```!
git diff --cached --stat 2>/dev/null || git diff --stat 2>/dev/null || echo "No changes"
```

Detailed diff:
```!
git diff --cached 2>/dev/null || git diff 2>/dev/null
```

Write a commit message with:
- **Subject**: imperative mood, under 72 characters, format `type(scope): subject`
- **Body**: explain the "why," not the "what" (the diff shows the what)
- **Footer**: breaking changes or issue references if applicable

Types: `feat`, `fix`, `refactor`, `test`, `docs`, `chore`, `perf`, `ci`, `build`.

If the diff contains multiple unrelated changes, suggest splitting into separate commits.

Output ONLY the commit message, ready to copy-paste.
