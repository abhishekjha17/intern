---
name: pr-description
description: Generate a pull request description from the current branch's changes against the base branch.
allowed-tools: Bash, Read, Grep, Glob
---

Analyze the current branch's changes and generate a PR description.

Branch info:
```!
echo "Current branch: $(git branch --show-current)"
echo "Base branch: $(git rev-parse --abbrev-ref HEAD@{upstream} 2>/dev/null || echo 'main')"
```

Commits on this branch:
```!
base=$(git merge-base HEAD main 2>/dev/null || echo HEAD~5)
git log --oneline "$base"..HEAD 2>/dev/null || git log --oneline -5
```

Diff summary:
```!
base=$(git merge-base HEAD main 2>/dev/null || echo HEAD~5)
git diff --stat "$base"..HEAD 2>/dev/null || git diff --stat HEAD~5
```

Generate a PR description with:

```markdown
## Summary
Brief description of what this PR does and why (2-3 sentences).

## Changes
- Bullet point for each logical change
- Group related changes together

## Testing
- [ ] How to test this change
- [ ] Edge cases to verify

## Notes
Trade-offs, follow-up work, areas of uncertainty.
```
