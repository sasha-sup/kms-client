# ROLE AND CONTEXT

You are a senior Go developer and security systems architect. Your task is to
perform a deep analysis of an existing repository and implement new functionality
in the branch `feature/dead-man-switch-heartbeat`.

IMPORTANT: All your responses, explanations, plans, and comments must be in Russian.
Code, identifiers, comments inside code, and file names remain in English.

---

# STEP 1 — REPOSITORY ANALYSIS

Before writing any code, perform a full analysis of the repository directory:

1. Read the full project structure (all files and directories)
2. Find and study:
   - go.mod / go.sum — dependencies and Go version
   - All existing .go files — structs, interfaces, HTTP/gRPC handlers
   - Configuration files (yaml, toml, env.example)
   - Dockerfile, Makefile, CI/CD configs
   - README and any documentation
   - Existing KMS logic — how the server issues keys for LUKS decryption
3. Build an architectural map: what already exists, how components are connected,
   what extension points are available
4. Find the entry point for LUKS decryption key requests —
   this is the critical integration point for node status checking

Do not skip this step. All subsequent code must fit organically into the existing
codebase, not conflict with it.

After completing the analysis, write a summary in Russian:
- Project structure overview
- How the existing KMS protocol works
- Where exactly the new code will be integrated
- Which storage library you will use and why

---

# STEP 2 — ARCHITECTURAL DECISION

Based on the analysis, propose and document decisions on the following points
before writing any code. Write your reasoning in Russian.

**Node state storage:**
Choose the most appropriate embedded solution for Go (preferably SQLite via
modernc.org/sqlite or bbolt). Justify the choice based on what is already used
in the project. The schema must at minimum contain:
- node_uuid (PK)
- node_ip
- first_seen (timestamp)
- last_heartbeat (timestamp)
- status: ENUM(active, blocked)
- blocked_at (timestamp, nullable)
- block_reason (string)

**Heartbeat authentication:**
Since the system operates in a closed network and the agent runs as a Kubernetes
DaemonSet, implement authentication via HMAC-SHA256 with a pre-shared key
provided through the env variable `HEARTBEAT_HMAC_KEY`.
Each heartbeat request must include an HMAC signature over
`node_uuid + node_ip + unix_timestamp` (rounded to 30s window to tolerate
clock skew). If the repo already has an auth mechanism — reuse it.

---

# STEP 3 — WHAT TO IMPLEMENT

## 3.1 KMS Server — changes

### Node registration on first request
In the existing LUKS key handler (find it during analysis):
- Extract `node_uuid` and `node_ip` from the request
  (headers or body — follow the existing protocol in the repo)
- If node is not registered — create a record with status `active`
- If node is registered and status is `blocked` — immediately return
  HTTP 403 with body `{"error": "node_blocked", "reason": "<block_reason>"}`
  and log the attempt
- If node is `active` — proceed with the normal key issuance flow

### New HTTP endpoint: `POST /heartbeat`
Request body:
```json
{
  "node_uuid": "string",
  "node_ip": "string",
  "timestamp": 1234567890
}
```
Logic:
- Verify HMAC authentication — reject with 401 if invalid
- If node status is `blocked` — return 403, do not update last_heartbeat
- If node is `active` — update `last_heartbeat = now()`
- If node does not exist — create a record (node hasn't requested a key yet)
- Return 200 with `{"status": "ok", "next_heartbeat_in": N}`

### Dead Man's Switch — background goroutine
Started at server startup. Every `HEARTBEAT_CHECK_INTERVAL` seconds
(default: 30s, configurable via env) checks all `active` nodes:
- If `now() - last_heartbeat > HEARTBEAT_TIMEOUT` (default: 120s, env)
- Update status → `blocked`, `blocked_at = now()`,
  `block_reason = "heartbeat_timeout"`
- Log: `[SECURITY] node %s (%s) blocked: no heartbeat for %ds`

### Manual unblock endpoint: `POST /admin/nodes/{uuid}/unblock`
- Protected by a separate `ADMIN_TOKEN` (env variable, required)
- Resets status → `active`, clears `blocked_at` and `block_reason`
- Sets `last_heartbeat = now()` (grace period after unblock)
- Logs the operator action

### Node list endpoint: `GET /admin/nodes`
- Returns all nodes: uuid, ip, status, last_heartbeat, blocked_at, block_reason
- Protected by the same `ADMIN_TOKEN`

### Prometheus metrics (only if /metrics already exists in the repo):
kms_nodes_total{status="active|blocked"}
kms_node_last_heartbeat_seconds{node_uuid="..."}
kms_heartbeat_timeouts_total

## 3.2 KMS Agent — Kubernetes DaemonSet

Create a new component at `cmd/kms-agent/` (or `agent/` — follow the repo structure):

### Agent binary (Go)
- On startup: read `NODE_UUID` and `NODE_IP` from env
  (injected in DaemonSet via fieldRef from Pod spec)
- Run a loop: every `HEARTBEAT_INTERVAL` seconds (default: 30s, env)
  send POST to `KMS_SERVER_URL/heartbeat` with HMAC signature
- On connection error — log and retry with exponential backoff
  (do not stop — network may be temporarily unavailable)
- Graceful shutdown on SIGTERM

### Kubernetes manifests (`deploy/kms-agent/`)

**daemonset.yaml:**
- DaemonSet with tolerations for all nodes including control-plane
- env from fieldRef:
    - NODE_IP: status.podIP
    - NODE_UUID: spec.nodeName (investigate where Talos exposes the actual
      machine UUID and document your finding in Russian)
- KMS_SERVER_URL from ConfigMap
- HEARTBEAT_HMAC_KEY from Secret

**configmap.yaml** — agent configuration

**secret.yaml** — template with placeholder values, never real secrets

**serviceaccount.yaml + rbac.yaml** — minimal required permissions

---

# STEP 4 — SERVER CONFIGURATION

Add to KMS server configuration (env / config file — follow existing pattern):
HEARTBEAT_INTERVAL=30           # seconds between agent heartbeats
HEARTBEAT_TIMEOUT=120           # seconds without heartbeat before blocking
HEARTBEAT_CHECK_INTERVAL=30     # how often server checks for timeouts
HEARTBEAT_HMAC_KEY=<secret>     # required — panic on startup if missing
ADMIN_TOKEN=<secret>            # required — panic on startup if missing
DB_PATH=./kms.db                # path to SQLite file

---

# STEP 5 — TESTS

Write unit tests for:
1. Dead man's switch goroutine — mock time, verify node is blocked
   exactly after the timeout window
2. `POST /heartbeat` handler — blocked node gets 403, active node is updated
3. LUKS key handler — blocked node gets 403 instead of a key
4. `POST /admin/nodes/{uuid}/unblock` — 401 without token, unblocks with token

---

# STEP 6 — DOCUMENTATION

Update or create:
- `README.md` — section "Dead Man's Switch Heartbeat" describing the mechanism,
  environment variables, and example requests (write documentation in Russian)
- `deploy/kms-agent/README.md` — DaemonSet deployment instructions (in Russian)

---

# RULES AND CONSTRAINTS

- Do not break existing functionality — everything that worked before must
  still work
- Do not add external dependencies without strong justification —
  Go stdlib + what is already in go.mod
- All work must be done in branch `feature/dead-man-switch-heartbeat`
- Idiomatic Go — interfaces, error wrapping, context propagation
- No hardcoded secrets — environment variables only
- Closed network — no external CA TLS verification needed, but mTLS between
  agent and server is welcome if the infrastructure supports it
- After implementation run `go build ./...` and `go test ./...` and confirm
  everything compiles and tests pass

---

# FINAL REPORT

After completion, write a report in Russian containing:
1. List of all modified and created files
2. Database schema chosen and rationale
3. How to run and verify the functionality locally
4. What requires manual configuration when deploying to the cluster
5. Known limitations and what could be improved next# ROLE AND CONTEXT

You are a senior Go developer and security systems architect. Your task is to
perform a deep analysis of an existing repository and implement new functionality
in the branch `feature/dead-man-switch-heartbeat`.

IMPORTANT: All your responses, explanations, plans, and comments must be in Russian.
Code, identifiers, comments inside code, and file names remain in English.

---

# STEP 1 — REPOSITORY ANALYSIS

Before writing any code, perform a full analysis of the repository directory:

1. Read the full project structure (all files and directories)
2. Find and study:
   - go.mod / go.sum — dependencies and Go version
   - All existing .go files — structs, interfaces, HTTP/gRPC handlers
   - Configuration files (yaml, toml, env.example)
   - Dockerfile, Makefile, CI/CD configs
   - README and any documentation
   - Existing KMS logic — how the server issues keys for LUKS decryption
3. Build an architectural map: what already exists, how components are connected,
   what extension points are available
4. Find the entry point for LUKS decryption key requests —
   this is the critical integration point for node status checking

Do not skip this step. All subsequent code must fit organically into the existing
codebase, not conflict with it.

After completing the analysis, write a summary in Russian:
- Project structure overview
- How the existing KMS protocol works
- Where exactly the new code will be integrated
- Which storage library you will use and why

---

# STEP 2 — ARCHITECTURAL DECISION

Based on the analysis, propose and document decisions on the following points
before writing any code. Write your reasoning in Russian.

**Node state storage:**
Choose the most appropriate embedded solution for Go (preferably SQLite via
modernc.org/sqlite or bbolt). Justify the choice based on what is already used
in the project. The schema must at minimum contain:
- node_uuid (PK)
- node_ip
- first_seen (timestamp)
- last_heartbeat (timestamp)
- status: ENUM(active, blocked)
- blocked_at (timestamp, nullable)
- block_reason (string)

**Heartbeat authentication:**
Since the system operates in a closed network and the agent runs as a Kubernetes
DaemonSet, implement authentication via HMAC-SHA256 with a pre-shared key
provided through the env variable `HEARTBEAT_HMAC_KEY`.
Each heartbeat request must include an HMAC signature over
`node_uuid + node_ip + unix_timestamp` (rounded to 30s window to tolerate
clock skew). If the repo already has an auth mechanism — reuse it.

---

# STEP 3 — WHAT TO IMPLEMENT

## 3.1 KMS Server — changes

### Node registration on first request
In the existing LUKS key handler (find it during analysis):
- Extract `node_uuid` and `node_ip` from the request
  (headers or body — follow the existing protocol in the repo)
- If node is not registered — create a record with status `active`
- If node is registered and status is `blocked` — immediately return
  HTTP 403 with body `{"error": "node_blocked", "reason": "<block_reason>"}`
  and log the attempt
- If node is `active` — proceed with the normal key issuance flow

### New HTTP endpoint: `POST /heartbeat`
Request body:
```json
{
  "node_uuid": "string",
  "node_ip": "string",
  "timestamp": 1234567890
}
```
Logic:
- Verify HMAC authentication — reject with 401 if invalid
- If node status is `blocked` — return 403, do not update last_heartbeat
- If node is `active` — update `last_heartbeat = now()`
- If node does not exist — create a record (node hasn't requested a key yet)
- Return 200 with `{"status": "ok", "next_heartbeat_in": N}`

### Dead Man's Switch — background goroutine
Started at server startup. Every `HEARTBEAT_CHECK_INTERVAL` seconds
(default: 30s, configurable via env) checks all `active` nodes:
- If `now() - last_heartbeat > HEARTBEAT_TIMEOUT` (default: 120s, env)
- Update status → `blocked`, `blocked_at = now()`,
  `block_reason = "heartbeat_timeout"`
- Log: `[SECURITY] node %s (%s) blocked: no heartbeat for %ds`

### Manual unblock endpoint: `POST /admin/nodes/{uuid}/unblock`
- Protected by a separate `ADMIN_TOKEN` (env variable, required)
- Resets status → `active`, clears `blocked_at` and `block_reason`
- Sets `last_heartbeat = now()` (grace period after unblock)
- Logs the operator action

### Node list endpoint: `GET /admin/nodes`
- Returns all nodes: uuid, ip, status, last_heartbeat, blocked_at, block_reason
- Protected by the same `ADMIN_TOKEN`

### Prometheus metrics (only if /metrics already exists in the repo):
kms_nodes_total{status="active|blocked"}
kms_node_last_heartbeat_seconds{node_uuid="..."}
kms_heartbeat_timeouts_total

## 3.2 KMS Agent — Kubernetes DaemonSet

Create a new component at `cmd/kms-agent/` (or `agent/` — follow the repo structure):

### Agent binary (Go)
- On startup: read `NODE_UUID` and `NODE_IP` from env
  (injected in DaemonSet via fieldRef from Pod spec)
- Run a loop: every `HEARTBEAT_INTERVAL` seconds (default: 30s, env)
  send POST to `KMS_SERVER_URL/heartbeat` with HMAC signature
- On connection error — log and retry with exponential backoff
  (do not stop — network may be temporarily unavailable)
- Graceful shutdown on SIGTERM

### Kubernetes manifests (`deploy/kms-agent/`)

**daemonset.yaml:**
- DaemonSet with tolerations for all nodes including control-plane
- env from fieldRef:
    - NODE_IP: status.podIP
    - NODE_UUID: spec.nodeName (investigate where Talos exposes the actual
      machine UUID and document your finding in Russian)
- KMS_SERVER_URL from ConfigMap
- HEARTBEAT_HMAC_KEY from Secret

**configmap.yaml** — agent configuration

**secret.yaml** — template with placeholder values, never real secrets

**serviceaccount.yaml + rbac.yaml** — minimal required permissions

---

# STEP 4 — SERVER CONFIGURATION

Add to KMS server configuration (env / config file — follow existing pattern):
HEARTBEAT_INTERVAL=30           # seconds between agent heartbeats
HEARTBEAT_TIMEOUT=120           # seconds without heartbeat before blocking
HEARTBEAT_CHECK_INTERVAL=30     # how often server checks for timeouts
HEARTBEAT_HMAC_KEY=<secret>     # required — panic on startup if missing
ADMIN_TOKEN=<secret>            # required — panic on startup if missing
DB_PATH=./kms.db                # path to SQLite file

---

# STEP 5 — TESTS

Write unit tests for:
1. Dead man's switch goroutine — mock time, verify node is blocked
   exactly after the timeout window
2. `POST /heartbeat` handler — blocked node gets 403, active node is updated
3. LUKS key handler — blocked node gets 403 instead of a key
4. `POST /admin/nodes/{uuid}/unblock` — 401 without token, unblocks with token

---

# STEP 6 — DOCUMENTATION

Update or create:
- `README.md` — section "Dead Man's Switch Heartbeat" describing the mechanism,
  environment variables, and example requests (write documentation in Russian)
- `deploy/kms-agent/README.md` — DaemonSet deployment instructions (in Russian)

---

# RULES AND CONSTRAINTS

- Do not break existing functionality — everything that worked before must
  still work
- Do not add external dependencies without strong justification —
  Go stdlib + what is already in go.mod
- All work must be done in branch `feature/dead-man-switch-heartbeat`
- Idiomatic Go — interfaces, error wrapping, context propagation
- No hardcoded secrets — environment variables only
- Closed network — no external CA TLS verification needed, but mTLS between
  agent and server is welcome if the infrastructure supports it
- After implementation run `go build ./...` and `go test ./...` and confirm
  everything compiles and tests pass

---

# FINAL REPORT

After completion, write a report in Russian containing:
1. List of all modified and created files
2. Database schema chosen and rationale
3. How to run and verify the functionality locally
4. What requires manual configuration when deploying to the cluster
5. Known limitations and what could be improved next# ROLE AND CONTEXT

You are a senior Go developer and security systems architect. Your task is to
perform a deep analysis of an existing repository and implement new functionality
in the branch `feature/dead-man-switch-heartbeat`.

IMPORTANT: All your responses, explanations, plans, and comments must be in Russian.
Code, identifiers, comments inside code, and file names remain in English.

---

# STEP 1 — REPOSITORY ANALYSIS

Before writing any code, perform a full analysis of the repository directory:

1. Read the full project structure (all files and directories)
2. Find and study:
   - go.mod / go.sum — dependencies and Go version
   - All existing .go files — structs, interfaces, HTTP/gRPC handlers
   - Configuration files (yaml, toml, env.example)
   - Dockerfile, Makefile, CI/CD configs
   - README and any documentation
   - Existing KMS logic — how the server issues keys for LUKS decryption
3. Build an architectural map: what already exists, how components are connected,
   what extension points are available
4. Find the entry point for LUKS decryption key requests —
   this is the critical integration point for node status checking

Do not skip this step. All subsequent code must fit organically into the existing
codebase, not conflict with it.

After completing the analysis, write a summary in Russian:
- Project structure overview
- How the existing KMS protocol works
- Where exactly the new code will be integrated
- Which storage library you will use and why

---

# STEP 2 — ARCHITECTURAL DECISION

Based on the analysis, propose and document decisions on the following points
before writing any code. Write your reasoning in Russian.

**Node state storage:**
Choose the most appropriate embedded solution for Go (preferably SQLite via
modernc.org/sqlite or bbolt). Justify the choice based on what is already used
in the project. The schema must at minimum contain:
- node_uuid (PK)
- node_ip
- first_seen (timestamp)
- last_heartbeat (timestamp)
- status: ENUM(active, blocked)
- blocked_at (timestamp, nullable)
- block_reason (string)

**Heartbeat authentication:**
Since the system operates in a closed network and the agent runs as a Kubernetes
DaemonSet, implement authentication via HMAC-SHA256 with a pre-shared key
provided through the env variable `HEARTBEAT_HMAC_KEY`.
Each heartbeat request must include an HMAC signature over
`node_uuid + node_ip + unix_timestamp` (rounded to 30s window to tolerate
clock skew). If the repo already has an auth mechanism — reuse it.

---

# STEP 3 — WHAT TO IMPLEMENT

## 3.1 KMS Server — changes

### Node registration on first request
In the existing LUKS key handler (find it during analysis):
- Extract `node_uuid` and `node_ip` from the request
  (headers or body — follow the existing protocol in the repo)
- If node is not registered — create a record with status `active`
- If node is registered and status is `blocked` — immediately return
  HTTP 403 with body `{"error": "node_blocked", "reason": "<block_reason>"}`
  and log the attempt
- If node is `active` — proceed with the normal key issuance flow

### New HTTP endpoint: `POST /heartbeat`
Request body:
```json
{
  "node_uuid": "string",
  "node_ip": "string",
  "timestamp": 1234567890
}
```
Logic:
- Verify HMAC authentication — reject with 401 if invalid
- If node status is `blocked` — return 403, do not update last_heartbeat
- If node is `active` — update `last_heartbeat = now()`
- If node does not exist — create a record (node hasn't requested a key yet)
- Return 200 with `{"status": "ok", "next_heartbeat_in": N}`

### Dead Man's Switch — background goroutine
Started at server startup. Every `HEARTBEAT_CHECK_INTERVAL` seconds
(default: 30s, configurable via env) checks all `active` nodes:
- If `now() - last_heartbeat > HEARTBEAT_TIMEOUT` (default: 120s, env)
- Update status → `blocked`, `blocked_at = now()`,
  `block_reason = "heartbeat_timeout"`
- Log: `[SECURITY] node %s (%s) blocked: no heartbeat for %ds`

### Manual unblock endpoint: `POST /admin/nodes/{uuid}/unblock`
- Protected by a separate `ADMIN_TOKEN` (env variable, required)
- Resets status → `active`, clears `blocked_at` and `block_reason`
- Sets `last_heartbeat = now()` (grace period after unblock)
- Logs the operator action

### Node list endpoint: `GET /admin/nodes`
- Returns all nodes: uuid, ip, status, last_heartbeat, blocked_at, block_reason
- Protected by the same `ADMIN_TOKEN`

### Prometheus metrics (only if /metrics already exists in the repo):
kms_nodes_total{status="active|blocked"}
kms_node_last_heartbeat_seconds{node_uuid="..."}
kms_heartbeat_timeouts_total

## 3.2 KMS Agent — Kubernetes DaemonSet

Create a new component at `cmd/kms-agent/` (or `agent/` — follow the repo structure):

### Agent binary (Go)
- On startup: read `NODE_UUID` and `NODE_IP` from env
  (injected in DaemonSet via fieldRef from Pod spec)
- Run a loop: every `HEARTBEAT_INTERVAL` seconds (default: 30s, env)
  send POST to `KMS_SERVER_URL/heartbeat` with HMAC signature
- On connection error — log and retry with exponential backoff
  (do not stop — network may be temporarily unavailable)
- Graceful shutdown on SIGTERM

### Kubernetes manifests (`deploy/kms-agent/`)

**daemonset.yaml:**
- DaemonSet with tolerations for all nodes including control-plane
- env from fieldRef:
    - NODE_IP: status.podIP
    - NODE_UUID: spec.nodeName (investigate where Talos exposes the actual
      machine UUID and document your finding in Russian)
- KMS_SERVER_URL from ConfigMap
- HEARTBEAT_HMAC_KEY from Secret

**configmap.yaml** — agent configuration

**secret.yaml** — template with placeholder values, never real secrets

**serviceaccount.yaml + rbac.yaml** — minimal required permissions

---

# STEP 4 — SERVER CONFIGURATION

Add to KMS server configuration (env / config file — follow existing pattern):
HEARTBEAT_INTERVAL=30           # seconds between agent heartbeats
HEARTBEAT_TIMEOUT=120           # seconds without heartbeat before blocking
HEARTBEAT_CHECK_INTERVAL=30     # how often server checks for timeouts
HEARTBEAT_HMAC_KEY=<secret>     # required — panic on startup if missing
ADMIN_TOKEN=<secret>            # required — panic on startup if missing
DB_PATH=./kms.db                # path to SQLite file

---

# STEP 5 — TESTS

Write unit tests for:
1. Dead man's switch goroutine — mock time, verify node is blocked
   exactly after the timeout window
2. `POST /heartbeat` handler — blocked node gets 403, active node is updated
3. LUKS key handler — blocked node gets 403 instead of a key
4. `POST /admin/nodes/{uuid}/unblock` — 401 without token, unblocks with token

---

# STEP 6 — DOCUMENTATION

Update or create:
- `README.md` — section "Dead Man's Switch Heartbeat" describing the mechanism,
  environment variables, and example requests (write documentation in Russian)
- `deploy/kms-agent/README.md` — DaemonSet deployment instructions (in Russian)

---

# RULES AND CONSTRAINTS

- Do not break existing functionality — everything that worked before must
  still work
- Do not add external dependencies without strong justification —
  Go stdlib + what is already in go.mod
- All work must be done in branch `feature/dead-man-switch-heartbeat`
- Idiomatic Go — interfaces, error wrapping, context propagation
- No hardcoded secrets — environment variables only
- Closed network — no external CA TLS verification needed, but mTLS between
  agent and server is welcome if the infrastructure supports it
- After implementation run `go build ./...` and `go test ./...` and confirm
  everything compiles and tests pass

---

# FINAL REPORT

After completion, write a report in Russian containing:
1. List of all modified and created files
2. Database schema chosen and rationale
3. How to run and verify the functionality locally
4. What requires manual configuration when deploying to the cluster
5. Known limitations and what could be improved next