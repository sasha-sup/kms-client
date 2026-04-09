<!-- Parent: ../AGENTS.md -->
<!-- Generated: 2026-04-09 | Updated: 2026-04-09 -->

# hack

## Purpose

Build and release helper scripts used by the Makefile and CI.

## Key Files

| File | Description |
|------|-------------|
| `release.sh` | Generates release notes from git history |
| `release.toml` | Release notes configuration (changelog groups) |
| `govulncheck.sh` | Wrapper for running govulncheck with proper settings |

## For AI Agents

### Working In This Directory

- Scripts are invoked by Makefile targets, not directly
- `release.sh` is used in `make release-notes` for GitHub Releases

<!-- MANUAL: -->
