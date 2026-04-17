---
name: find-bugs
description: Hunt for bugs in code — logic errors, race conditions, nil dereferences, resource leaks, edge cases.
argument-hint: [file-or-path]
allowed-tools: Read, Grep, Glob, Bash
agent: bug-finder
---

Find bugs in the code specified by `$ARGUMENTS`. If no path is given, analyze the current git diff.

Current changes for context:
```!
git diff --stat HEAD 2>/dev/null || echo "No git changes"
```

Focus on correctness issues: logic errors, race conditions, nil/null dereferences, resource leaks, off-by-one bugs, error handling gaps.

Report each finding with location, severity, trigger conditions, impact, and suggested fix. Sort by severity.
