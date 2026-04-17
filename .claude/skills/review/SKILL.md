---
name: review
description: Run a thorough code review on files or a git diff. Finds bugs, style issues, security concerns, and performance problems.
argument-hint: [file-or-path]
allowed-tools: Read, Grep, Glob, Bash
agent: code-reviewer
---

Review the code specified by the user. If no specific file is given, review the current git diff (`git diff` for unstaged, `git diff --cached` for staged).

Current git status for context:
```!
git diff --stat HEAD 2>/dev/null || echo "No git changes"
```

If the user provided a file path via `$ARGUMENTS`, review that file. Otherwise, review the current staged or unstaged changes.

Provide findings ranked by severity (critical > warning > info) with specific line references and suggested fixes.
