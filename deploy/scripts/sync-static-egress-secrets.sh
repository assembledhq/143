#!/usr/bin/env bash
set -euo pipefail

# Synchronize generated static egress WireGuard config in .env.production.enc.
# FLEET_HOSTS is the source of truth: every worker:<host> entry gets a stable
# tunnel private key, tunnel IP, and gateway peer. Existing worker keys/IPs are
# preserved so re-running this helper is safe.

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
ENC_FILE="${STATIC_EGRESS_ENV_FILE:-$PROJECT_DIR/.env.production.enc}"
APPLY=0
PROVISION_WORKER_HOST="${PROVISION_WORKER_HOST:-}"
TUNNEL_PREFIX="${STATIC_EGRESS_TUNNEL_PREFIX:-10.143.0}"
FIRST_WORKER_OCTET="${STATIC_EGRESS_FIRST_WORKER_OCTET:-2}"
DEFAULT_GATEWAY_ADDRESS="${STATIC_EGRESS_GATEWAY_WG_ADDRESS:-${TUNNEL_PREFIX}.1/24}"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --apply)
      APPLY=1
      ;;
    *)
      echo "Unknown argument: $1" >&2
      echo "Usage: $0 [--apply]" >&2
      exit 1
      ;;
  esac
  shift
done

require_command() {
  local command="$1"
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "ERROR: $command is required." >&2
    exit 1
  fi
}

dotenv_get() {
  local key="$1"
  local file="$2"
  grep -E "^${key}=" "$file" 2>/dev/null | tail -n 1 | cut -d= -f2- || true
}

# json_encode_string emits a JSON string literal for use as a `sops set` value.
# WireGuard keys, IPs, and host strings never contain control characters, but we
# still escape backslashes and double quotes so the value is always valid JSON.
json_encode_string() {
  local s="$1"
  s="${s//\\/\\\\}"
  s="${s//\"/\\\"}"
  printf '"%s"' "$s"
}

# sops_set edits a single dotenv key in place in the given file. In-place edits
# preserve the SOPS data key, so only the touched value changes in the
# ciphertext and unrelated secrets stay byte-identical (a reviewable diff). A
# full decrypt+re-encrypt, by contrast, mints a new data key and rewrites every
# value. --idempotent skips keys whose value is unchanged so no-op re-runs
# produce no diff, and --value-stdin keeps secrets out of the process listing.
sops_set() {
  local key="$1"
  local value="$2"
  local file="$3"
  json_encode_string "$value" | sops set \
    --input-type dotenv --output-type dotenv --idempotent --value-stdin \
    "$file" "[\"${key}\"]"
}

generate_private_key() {
  wg genkey
}

derive_public_key() {
  local private_key="$1"
  printf '%s\n' "$private_key" | wg pubkey
}

trim_spaces() {
  local value="$1"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  printf '%s' "$value"
}

next_worker_address() {
  local octet="$FIRST_WORKER_OCTET"

  while [ "$octet" -le 254 ]; do
    if ! octet_is_used "$octet"; then
      mark_octet_used "$octet"
      next_worker_address_result="${TUNNEL_PREFIX}.${octet}/32"
      return
    fi
    octet=$((octet + 1))
  done

  echo "ERROR: worker tunnel address overflowed ${TUNNEL_PREFIX}.0/24" >&2
  exit 1
}

mark_octet_used() {
  used_octets="${used_octets}${1} "
}

octet_is_used() {
  case "$used_octets" in
    *" $1 "*) return 0 ;;
    *) return 1 ;;
  esac
}

existing_worker_config() {
  local target_host="$1"
  local entry map_host rest address private_key
  local lookup_entries

  [ -n "$existing_worker_hosts" ] || return 0
  IFS=',' read -r -a lookup_entries <<< "$existing_worker_hosts"

  for entry in "${lookup_entries[@]}"; do
    entry="$(trim_spaces "$entry")"
    [ -n "$entry" ] || continue
    map_host="${entry%%@*}"
    rest="${entry#*@}"
    address="${rest%%@*}"
    private_key="${rest#*@}"
    if [ "$map_host" = "$target_host" ]; then
      printf '%s@%s' "$address" "$private_key"
      return
    fi
  done
  return 0
}

require_command sops

if [ ! -f "$ENC_FILE" ]; then
  echo "Skipping static egress secret sync; $ENC_FILE does not exist."
  exit 0
fi

tmp_env="$(mktemp)"
staged_enc=""
trap 'rm -f "$tmp_env" "$staged_enc"' EXIT
sops --decrypt --input-type dotenv --output-type dotenv "$ENC_FILE" > "$tmp_env"

static_public_ip="$(dotenv_get STATIC_EGRESS_PUBLIC_IP "$tmp_env")"
if [ -z "$static_public_ip" ]; then
  echo "Static egress is not configured; STATIC_EGRESS_PUBLIC_IP is empty."
  exit 0
fi
if ! command -v wg >/dev/null 2>&1; then
  echo "ERROR: WireGuard tools are required to generate static egress keys, but 'wg' was not found." >&2
  echo "Install locally with: brew install wireguard-tools" >&2
  exit 1
fi

fleet_hosts="$(dotenv_get FLEET_HOSTS "$tmp_env")"
if [ -z "$fleet_hosts" ]; then
  echo "ERROR: FLEET_HOSTS is required to derive static egress workers." >&2
  exit 1
fi

declare -a worker_hosts
egress_host_count=0
seen_worker_hosts=","
IFS=',' read -r -a fleet_entries <<< "$fleet_hosts"
for entry in "${fleet_entries[@]}"; do
  entry="$(trim_spaces "$entry")"
  [ -n "$entry" ] || continue
  role="${entry%%:*}"
  host="${entry#*:}"
  if [ "$role" = "egress" ] && [ -n "$host" ] && [ "$host" != "$entry" ]; then
    egress_host_count=$((egress_host_count + 1))
  fi
  if [ "$role" = "worker" ] && [ -n "$host" ] && [ "$host" != "$entry" ]; then
    case "$seen_worker_hosts" in
      *",$host,"*)
        echo "ERROR: duplicate worker:<host> entry in FLEET_HOSTS: worker:$host" >&2
        exit 1
        ;;
    esac
    seen_worker_hosts="${seen_worker_hosts}${host},"
    worker_hosts+=("$host")
  fi
done

if [ "$egress_host_count" -ne 1 ]; then
  echo "ERROR: FLEET_HOSTS must include exactly one egress:<host> entry when STATIC_EGRESS_PUBLIC_IP is configured." >&2
  exit 1
fi

if [ "${#worker_hosts[@]}" -eq 0 ]; then
  echo "ERROR: FLEET_HOSTS has no worker:<host> entries." >&2
  exit 1
fi

if [ -n "$PROVISION_WORKER_HOST" ]; then
  found_requested_worker=0
  for host in "${worker_hosts[@]}"; do
    if [ "$host" = "$PROVISION_WORKER_HOST" ]; then
      found_requested_worker=1
      break
    fi
  done
  if [ "$found_requested_worker" -ne 1 ]; then
    echo "ERROR: provision worker $PROVISION_WORKER_HOST is not in FLEET_HOSTS." >&2
    echo "Add worker:$PROVISION_WORKER_HOST to FLEET_HOSTS in .env.production.enc first." >&2
    exit 1
  fi
fi

used_octets=" "

existing_worker_hosts="$(dotenv_get STATIC_EGRESS_WORKER_HOSTS "$tmp_env")"
if [ -n "$existing_worker_hosts" ]; then
  IFS=',' read -r -a existing_entries <<< "$existing_worker_hosts"
  for entry in "${existing_entries[@]}"; do
    entry="$(trim_spaces "$entry")"
    [ -n "$entry" ] || continue
    map_host="${entry%%@*}"
    rest="${entry#*@}"
    if [ "$rest" = "$entry" ] || [[ "$rest" != *@* ]]; then
      echo "ERROR: invalid STATIC_EGRESS_WORKER_HOSTS entry '$entry'; expected host@wg-address@private-key" >&2
      exit 1
    fi
    address="${rest%%@*}"
    private_key="${rest#*@}"
    ip_without_cidr="${address%/32}"
    octet="${ip_without_cidr##*.}"
    if [[ "$address" == "${TUNNEL_PREFIX}."*"/32" ]] && [[ "$octet" =~ ^[0-9]+$ ]]; then
      mark_octet_used "$octet"
    fi
  done
fi

gateway_private_key="$(dotenv_get STATIC_EGRESS_GATEWAY_PRIVATE_KEY "$tmp_env")"
if [ -z "$gateway_private_key" ]; then
  gateway_private_key="$(generate_private_key)"
fi
gateway_public_key="$(derive_public_key "$gateway_private_key")"
gateway_public_ip="$(dotenv_get STATIC_EGRESS_GATEWAY_PUBLIC_IP "$tmp_env")"
if [ -z "$gateway_public_ip" ]; then
  gateway_public_ip="$static_public_ip"
fi
gateway_wg_address="$(dotenv_get STATIC_EGRESS_GATEWAY_WG_ADDRESS "$tmp_env")"
if [ -z "$gateway_wg_address" ]; then
  gateway_wg_address="$DEFAULT_GATEWAY_ADDRESS"
fi

worker_map=""
worker_peers=""
generated_workers=0

for host in "${worker_hosts[@]}"; do
  existing_config="$(existing_worker_config "$host")"
  address=""
  private_key=""
  if [ -n "$existing_config" ]; then
    address="${existing_config%%@*}"
    private_key="${existing_config#*@}"
  fi
  if [ -z "$address" ]; then
    next_worker_address
    address="$next_worker_address_result"
  fi
  if [ -z "$private_key" ]; then
    private_key="$(generate_private_key)"
    generated_workers=$((generated_workers + 1))
  fi
  public_key="$(derive_public_key "$private_key")"

  if [ -n "$worker_map" ]; then
    worker_map+=","
    worker_peers+=","
  fi
  worker_map+="${host}@${address}@${private_key}"
  worker_peers+="${public_key}@${address}"
done

echo "Static egress sync derived ${#worker_hosts[@]} worker peer(s) from FLEET_HOSTS; generated $generated_workers new worker key(s)."

if [ "$APPLY" -ne 1 ]; then
  echo "Dry run only. Re-run with --apply to update $ENC_FILE."
  exit 0
fi

# Stage all edits on a copy and swap it in atomically. Editing a copy still
# preserves the SOPS data key (it is in-place editing, not a re-encrypt), so
# only the touched keys change in the ciphertext and unrelated secrets stay
# byte-identical. The staged copy lives next to $ENC_FILE so the final mv is an
# atomic rename on the same filesystem; if any sops_set fails, set -e aborts and
# the live $ENC_FILE is left untouched.
staged_enc="$(mktemp "${ENC_FILE}.XXXXXX")"
cp "$ENC_FILE" "$staged_enc"
sops_set STATIC_EGRESS_GATEWAY_PRIVATE_KEY "$gateway_private_key" "$staged_enc"
sops_set STATIC_EGRESS_GATEWAY_PUBLIC_KEY "$gateway_public_key" "$staged_enc"
sops_set STATIC_EGRESS_GATEWAY_PUBLIC_IP "$gateway_public_ip" "$staged_enc"
sops_set STATIC_EGRESS_GATEWAY_WG_ADDRESS "$gateway_wg_address" "$staged_enc"
sops_set STATIC_EGRESS_WORKER_HOSTS "$worker_map" "$staged_enc"
sops_set STATIC_EGRESS_WORKER_PEERS "$worker_peers" "$staged_enc"

if cmp -s "$staged_enc" "$ENC_FILE"; then
  echo "Static egress config already up to date; $ENC_FILE unchanged."
else
  mv "$staged_enc" "$ENC_FILE"
  staged_enc=""
  echo "Updated $ENC_FILE with generated static egress config."
fi
echo "Commit $ENC_FILE after provisioning succeeds so generated gateway and worker keys are preserved."
