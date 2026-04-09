#!/usr/bin/env bash

set -euo pipefail

BASE_URL="${KMS_BASE_URL:-}"
TOKEN="${KMS_ADMIN_TOKEN:-}"

usage() {
  cat <<'EOF'
Usage:
  kms-admin.sh list
  kms-admin.sh blocked
  kms-admin.sh unblock <node>
  kms-admin.sh maintenance <node> on|off

<node> can be a UUID or a hostname.

Environment:
  KMS_ADMIN_TOKEN  Required admin token for KMS admin API
  KMS_BASE_URL     Optional base URL

Examples:
  KMS_ADMIN_TOKEN=... ./scripts/kms-admin.sh list
  KMS_ADMIN_TOKEN=... ./scripts/kms-admin.sh blocked
  KMS_ADMIN_TOKEN=... ./scripts/kms-admin.sh unblock worker-1
  KMS_ADMIN_TOKEN=... ./scripts/kms-admin.sh maintenance worker-1 on
  KMS_ADMIN_TOKEN=... ./scripts/kms-admin.sh maintenance worker-1 off
EOF
}

require_token() {
  if [[ -z "${TOKEN}" ]]; then
    echo "error: KMS_ADMIN_TOKEN is required" >&2
    exit 1
  fi
}

api() {
  local method="$1"
  local path="$2"
  local data="${3:-}"

  if [[ -n "${data}" ]]; then
    curl -fsS \
      -X "${method}" \
      -H "Authorization: Bearer ${TOKEN}" \
      -H "Content-Type: application/json" \
      -d "${data}" \
      "${BASE_URL}${path}"
  else
    curl -fsS \
      -X "${method}" \
      -H "Authorization: Bearer ${TOKEN}" \
      "${BASE_URL}${path}"
  fi
}

# resolve_uuid accepts a node identifier (UUID or hostname) and returns the UUID.
# If the identifier matches a hostname, the UUID is looked up from the node list.
resolve_uuid() {
  local node="$1"
  local uuid

  uuid="$(api GET /admin/nodes | jq -r --arg node "${node}" '
    (.[] | select(.uuid == $node) | .uuid)
    // (.[] | select(.hostname == $node) | .uuid)
    // empty
  ')"

  if [[ -z "${uuid}" ]]; then
    echo "error: node not found: ${node}" >&2
    exit 1
  fi

  printf '%s' "${uuid}"
}

format_nodes_table() {
  jq -r '
    (["STATUS", "HOSTNAME", "UUID", "IP", "MAINTENANCE", "BLOCK_REASON", "LAST_HEARTBEAT"] | @tsv),
    (.[] | [
      (.status // "-"),
      (.hostname // "-"),
      (.uuid // "-"),
      (.ip // "-"),
      (if .maintenance then "yes" else "-" end),
      (.block_reason // "-"),
      (.last_heartbeat // "-")
    ] | @tsv)
  ' | column -t -s $'\t'
}

format_blocked_table() {
  local blocked
  blocked="$(jq '[.[] | select(.status == "blocked")]')"

  if [[ "${blocked}" == "[]" ]]; then
    echo "No blocked nodes"
    return 0
  fi

  printf '%s\n' "${blocked}" | format_nodes_table
}

cmd="${1:-}"

case "${cmd}" in
  list)
    require_token
    api GET /admin/nodes | format_nodes_table
    ;;
  blocked)
    require_token
    api GET /admin/nodes | format_blocked_table
    ;;
  unblock)
    require_token
    node="${2:-}"
    if [[ -z "${node}" ]]; then
      echo "error: node identifier (uuid or hostname) is required for unblock" >&2
      usage
      exit 1
    fi
    uuid="$(resolve_uuid "${node}")"
    echo "Unblocking node ${uuid}..."
    api POST "/admin/nodes/${uuid}/unblock" | jq .
    ;;
  maintenance)
    require_token
    node="${2:-}"
    toggle="${3:-}"
    if [[ -z "${node}" || -z "${toggle}" ]]; then
      echo "error: usage: kms-admin.sh maintenance <node> on|off" >&2
      usage
      exit 1
    fi
    case "${toggle}" in
      on)  enabled=true  ;;
      off) enabled=false ;;
      *)
        echo "error: expected 'on' or 'off', got '${toggle}'" >&2
        exit 1
        ;;
    esac
    uuid="$(resolve_uuid "${node}")"
    echo "Setting maintenance=${toggle} for node ${uuid}..."
    api POST "/admin/nodes/${uuid}/maintenance" "{\"enabled\":${enabled}}" | jq .
    ;;
  -h|--help|help|"")
    usage
    ;;
  *)
    echo "error: unknown command: ${cmd}" >&2
    usage
    exit 1
    ;;
esac
