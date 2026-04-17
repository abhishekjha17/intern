---
name: security-audit
description: Run a security audit on code — OWASP Top 10, injection, auth issues, data exposure, cryptographic misuse.
argument-hint: [file-or-path]
allowed-tools: Read, Grep, Glob, Bash
agent: security-auditor
---

Run a security audit on the code at `$ARGUMENTS`. If no path is given, audit the entire project.

Project structure for context:
```!
find . -name '*.go' -not -path './vendor/*' -not -path './.claude/*' | head -20
```

Focus on exploitable vulnerabilities with specific code paths. Report findings with severity, CWE IDs, and remediation steps.
