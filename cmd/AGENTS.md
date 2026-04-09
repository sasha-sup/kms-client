<!-- Parent: ../AGENTS.md -->
<!-- Generated: 2026-04-09 | Updated: 2026-04-09 -->

# cmd

## Purpose

Application entry points for two binaries: kms-server and kms-agent.

## Subdirectories

| Directory | Purpose |
|-----------|---------|
| `kms-server/` | KMS server binary — gRPC + HTTP + metrics (see `kms-server/AGENTS.md`) |
| `kms-agent/` | HTTP heartbeat agent for Kubernetes DaemonSet (see `kms-agent/AGENTS.md`) |

## For AI Agents

### Working In This Directory

- Each subdirectory is `package main` with its own binary
- Business logic lives in `pkg/server/` — cmd packages handle wiring and CLI flags

<!-- MANUAL: -->
