#!/usr/bin/env bash
# Shared helpers for the curl collection. Sourced, never run directly.

set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Load .env, but let anything already in the environment win so you can do
#   BASE_URL=http://staging:3000 ./01-health.sh
if [[ -f "$ROOT/.env" ]]; then
  while IFS= read -r line || [[ -n $line ]]; do
    [[ -z $line || $line == \#* || $line != *=* ]] && continue
    key=${line%%=*}
    [[ -n ${!key-} ]] && continue
    export "$key=${line#*=}"
  done < "$ROOT/.env"
fi

BASE_URL=${BASE_URL:-http://localhost:${PORT:-3000}}

# The gateway has two scoped credentials, so the collection carries both. `request` uses the
# console credential because most of these scripts drive the session lifecycle; `request_send`
# uses the dispatcher one, which is the only credential permitted to put a message on the wire.
if [[ -z ${DISPATCHER_API_KEY-} ]]; then
  echo "DISPATCHER_API_KEY is not set and no .env was found at $ROOT/.env" >&2
  echo "Run: cp .env.example .env  (then set real keys)" >&2
  exit 1
fi

# Falling back keeps the read-only scripts usable with only a dispatcher key; the scripts
# that manage sessions will correctly get a 403 in that case.
MANAGE_KEY=${CONSOLE_API_KEY:-$DISPATCHER_API_KEY}
SEND_KEY=$DISPATCHER_API_KEY

if [[ -t 1 ]]; then
  DIM=$'\033[2m'; GREEN=$'\033[32m'; RED=$'\033[31m'; BOLD=$'\033[1m'; OFF=$'\033[0m'
else
  DIM=''; GREEN=''; RED=''; BOLD=''; OFF=''
fi

HAS_JQ=0
command -v jq >/dev/null 2>&1 && HAS_JQ=1

LAST_STATUS=''
LAST_BODY=''
FAILURES=0

# 10-sessions.sh drops the session it created here so the later scripts can chain off it
# without the caller copying a uuid around.
SESSION_FILE="$ROOT/curl/.last-session"

save_session_id() {
  printf '%s\n' "$1" > "$SESSION_FILE"
}

# Resolves SESSION_ID from the environment first, then the file. Empty if neither exists.
load_session_id() {
  if [[ -n ${SESSION_ID:-} ]]; then
    printf '%s' "$SESSION_ID"
  elif [[ -f $SESSION_FILE ]]; then
    tr -d '[:space:]' < "$SESSION_FILE"
  fi
}

# Exit code run-all.sh reads as "not applicable", so a script that needs a real phone
# number does not read as a failure when it is simply not configured.
readonly EXIT_SKIPPED=78

skip() {
  printf '\n%s· skipped: %s%s\n' "$DIM" "$1" "$OFF"
  exit $EXIT_SKIPPED
}

heading() {
  printf '\n%s== %s ==%s\n' "$BOLD" "$1" "$OFF"
}

# request METHOD PATH [JSON_BODY]   — authenticated as the console (read + manage)
# Sets LAST_STATUS and LAST_BODY.
request() {
  _do_request "$1" "$2" "${3-}" auth
}

# request_send METHOD PATH [JSON_BODY]  — authenticated as the dispatcher (send + read)
request_send() {
  _do_request "$1" "$2" "${3-}" send
}

# request_noauth METHOD PATH [JSON_BODY]   — no Authorization header
request_noauth() {
  _do_request "$1" "$2" "${3-}" noauth
}

# request_badauth METHOD PATH [JSON_BODY]  — deliberately wrong bearer token
request_badauth() {
  _do_request "$1" "$2" "${3-}" badauth
}

_do_request() {
  local method=$1 path=$2 body=$3 mode=$4
  local url="$BASE_URL$path"
  local args=(-sS -X "$method" "$url" -w $'\n%{http_code}')

  case $mode in
    auth)    args+=(-H "Authorization: Bearer $MANAGE_KEY") ;;
    send)    args+=(-H "Authorization: Bearer $SEND_KEY") ;;
    badauth) args+=(-H "Authorization: Bearer not-the-real-key") ;;
    noauth)  ;;
  esac

  if [[ -n $body ]]; then
    args+=(-H 'Content-Type: application/json' -d "$body")
  fi

  printf '%s%s %s%s' "$DIM" "$method" "$url" "$OFF"
  [[ $mode != auth ]] && printf ' %s(%s)%s' "$DIM" "$mode" "$OFF"
  printf '\n'
  [[ -n $body ]] && printf '%s%s%s\n' "$DIM" "$body" "$OFF"

  local raw
  if ! raw=$(curl "${args[@]}" 2>&1); then
    printf '%s  request failed: %s%s\n' "$RED" "$raw" "$OFF"
    LAST_STATUS='000'
    LAST_BODY=''
    return 0
  fi

  LAST_STATUS=${raw##*$'\n'}
  LAST_BODY=${raw%$'\n'*}

  if [[ -n $LAST_BODY ]]; then
    if (( HAS_JQ )) && printf '%s' "$LAST_BODY" | jq -e . >/dev/null 2>&1; then
      printf '%s' "$LAST_BODY" | jq .
    else
      printf '%s\n' "$LAST_BODY"
    fi
  fi
}

# expect STATUS ["what this proves"]
expect() {
  local want=$1 note=${2-}
  if [[ $LAST_STATUS == "$want" ]]; then
    printf '%s  ✓ %s%s%s\n' "$GREEN" "$want" "${note:+ — $note}" "$OFF"
  else
    printf '%s  ✗ expected %s, got %s%s%s\n' "$RED" "$want" "$LAST_STATUS" "${note:+ — $note}" "$OFF"
    FAILURES=$((FAILURES + 1))
  fi
}

# expect_json_field .path value
expect_json_field() {
  local path=$1 want=$2 got
  if (( ! HAS_JQ )); then
    printf '%s  · skipped %s check (jq not installed)%s\n' "$DIM" "$path" "$OFF"
    return 0
  fi
  got=$(printf '%s' "$LAST_BODY" | jq -r "$path" 2>/dev/null)
  if [[ $got == "$want" ]]; then
    printf '%s  ✓ %s = %s%s\n' "$GREEN" "$path" "$want" "$OFF"
  else
    printf '%s  ✗ %s: expected %s, got %s%s\n' "$RED" "$path" "$want" "${got:-<none>}" "$OFF"
    FAILURES=$((FAILURES + 1))
  fi
}

finish() {
  if (( FAILURES == 0 )); then
    printf '\n%sall checks passed%s\n' "$GREEN" "$OFF"
  else
    printf '\n%s%d check(s) failed%s\n' "$RED" "$FAILURES" "$OFF"
  fi
  exit $(( FAILURES > 0 ? 1 : 0 ))
}
