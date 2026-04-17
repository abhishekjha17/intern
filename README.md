# intern

[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License: GPL-3.0](https://img.shields.io/badge/License-GPL--3.0-blue.svg)](LICENSE)
[![CI](https://github.com/abhishekjha17/intern/actions/workflows/ci.yml/badge.svg)](https://github.com/abhishekjha17/intern/actions/workflows/ci.yml)

A lightweight proxy and profiler for Anthropic Claude API sessions. Intern sits between your Claude client and the API, captures every interaction as structured traces, and gives you tools to analyze cost, token usage, tool calls, and conversation patterns.

## Features

- **Transparent Proxy** — Drop-in reverse proxy for the Anthropic Messages API. Clients (Claude Code, Cursor, etc.) connect without any configuration changes.
- **Trace Logging** — Every request/response pair is captured as a JSONL trace with token counts, timestamps, session IDs, and full request/response bodies.
- **Cost Analysis** — Per-model cost breakdown with input, output, cache read, and cache creation token pricing.
- **Session Profiling** — Per-message classification: conversation phase (exploration, execution, verification, planning), complexity level, dependency chain, and offload candidacy.
- **Thinking Analysis** — Tracks extended thinking usage: thinking blocks with text vs. signature-only (adaptive mode).
- **Offload Detection** — Identifies messages that could be handled by a local model (health checks, trivial tasks, tool continuations) with estimated savings.
- **Claude Code Skills & Agents** — Built-in slash commands and custom agents for code review, bug finding, test writing, security audits, and more.
- **Zero Dependencies** — Pure Go standard library. Single static binary.

## Installation

### Homebrew (macOS/Linux)

```bash
brew tap abhishekjha17/intern
brew install intern
```

### Go Install

```bash
go install github.com/abhishekjha17/intern/cmd/intern@latest
```

### From Source

```bash
git clone https://github.com/abhishekjha17/intern.git
cd intern
make build
```

### Binary Releases

Download pre-built binaries from [GitHub Releases](https://github.com/abhishekjha17/intern/releases).

## Quick Start

### Option A: Background Service (Recommended)

```bash
# Start the proxy as a background service (survives reboots)
brew services start intern

# Use your Claude client normally — point API base URL to http://localhost:11411
# All interactions are automatically traced

# Analyze your session
intern profile $(brew --prefix)/var/log/intern/traces.jsonl
```

### Option B: Manual

```bash
# Start the proxy in the foreground
intern proxy --port 11411

# Traces are written to ~/.intern/traces/traces.jsonl by default
intern profile ~/.intern/traces/traces.jsonl
```

## Usage

### `intern proxy`

Starts a transparent reverse proxy to the Anthropic API with trace logging.

```
intern proxy [flags]

Flags:
  --port   int     Port to listen on (default: 11411)
  --trace  string  Path to JSONL trace file (default: ~/.intern/traces/traces.jsonl)
```

Configure your client to use `http://localhost:11411` as the API base URL. The proxy forwards all requests to `api.anthropic.com` and logs both sides of each interaction.

When installed via Homebrew and run as a service (`brew services start intern`), traces are written to `$(brew --prefix)/var/log/intern/traces.jsonl` instead.

### `intern profile`

Analyzes trace files and produces a comprehensive report.

```
intern profile [flags] <trace-files...>

Flags:
  --json   Output as JSON instead of text tables
```

**Text output** includes:

- Cost report by model (input, output, cache read, cache creation, total, avg/msg)
- Token averages by model
- Tool usage with frequency bars
- Content block type breakdown
- Conversation phase distribution (exploration, execution, verification, planning, conversation)
- Complexity distribution (trivial, mechanical, reasoning, creative)
- Per-session summary (messages, cost, duration, models used)
- Thinking block analysis
- Offload candidates with estimated savings

**JSON output** (`--json`) gives you the full report as structured JSON for further processing.

**Example:**

```bash
# Analyze multiple trace files
intern profile traces_day1.jsonl traces_day2.jsonl

# Pipe JSON to other tools
intern profile --json session.jsonl | jq '.cost.grand_total'
```

### Version

```bash
intern --version
```

## Claude Code Skills & Agents

This project ships with built-in [Claude Code](https://claude.ai/code) skills (slash commands) and custom agents. When you clone this repo, they're automatically available in your Claude Code session.

### Skills (Slash Commands)

| Command | Description |
|---------|-------------|
| `/review [file]` | Code review with severity-ranked findings |
| `/explain [file]` | Step-by-step code explanation |
| `/commit-message` | Generate conventional commit message from current diff |
| `/pr-description` | Generate PR description from branch changes |
| `/find-bugs [file]` | Hunt for logic errors, race conditions, edge cases |
| `/refactor [file]` | Suggest structural improvements and code smell fixes |
| `/write-tests [file]` | Generate table-driven unit tests |
| `/write-docs [file]` | Generate godoc-style documentation |
| `/security-audit [file]` | OWASP-focused vulnerability review |

### Agents

Custom subagents available for delegation:

| Agent | Description |
|-------|-------------|
| `code-reviewer` | Thorough code review with bug, style, security, and performance analysis |
| `bug-finder` | Focused correctness analysis — logic errors, races, leaks |
| `refactor-advisor` | Code smell detection and refactoring suggestions |
| `security-auditor` | OWASP Top 10 and common vulnerability pattern detection |

Skills and agents are defined in `.claude/skills/` and `.claude/agents/` respectively.

## Trace Format

Each line in a trace file is a JSON object:

```json
{
  "timestamp": "2025-04-16T10:30:00Z",
  "session_id": "a1b2c3d4...",
  "model": "claude-opus-4-6",
  "input_tokens": 15000,
  "output_tokens": 500,
  "thinking_tokens": 200,
  "cache_read_tokens": 12000,
  "request": "...",
  "response": "..."
}
```

Session IDs are derived from the first message in a conversation, so all turns in a session share the same ID.

## Roadmap

- [ ] **Multi-model routing** — Classify requests by complexity and route simple ones to local models (Ollama) while passing complex ones to the cloud API
- [ ] **Configurable routing rules** — User-defined rules for routing based on tools, token count, or conversation phase
- [ ] **Web dashboard** — Browser-based visualization of session traces and cost trends
- [ ] **Real-time profiling** — Live cost and usage stats while the proxy is running
- [ ] **Budget alerts** — Configurable spending thresholds with notifications
- [ ] **Prompt caching optimization** — Suggestions for improving cache hit rates

## Contributing

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/my-feature`)
3. Make your changes
4. Run tests (`make test`)
5. Run linter (`make lint`)
6. Commit with a descriptive message
7. Open a pull request

## License

[GPL-3.0](LICENSE)
