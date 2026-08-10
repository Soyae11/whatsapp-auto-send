#!/usr/bin/env bash
# /metrics and per-session health — the two things that answer "why did this message fail".
source "$(dirname "${BASH_SOURCE[0]}")/_common.sh"

heading "metrics require a token"
LAST_STATUS=$(curl -sS -o /dev/null -w '%{http_code}' "$BASE_URL/metrics")
printf '%s\n' "$LAST_STATUS"
expect 401

heading "prometheus exposition"
RAW=$(curl -sS -w '\n%{http_code}' -H "Authorization: Bearer $MANAGE_KEY" "$BASE_URL/metrics")
LAST_STATUS=${RAW##*$'\n'}
BODY=${RAW%$'\n'*}
expect 200

printf '%s\n' "$BODY" | grep -E '^(# TYPE|baileys_uptime|baileys_sessions_total)' | head -8 | sed 's/^/  /'

for metric in baileys_uptime_seconds baileys_sessions_total baileys_session_up baileys_session_status; do
  if printf '%s' "$BODY" | grep -q "^$metric"; then
    printf '%s  ✓ %s%s\n' "$GREEN" "$metric" "$OFF"
  else
    printf '%s  ✗ %s missing%s\n' "$RED" "$metric" "$OFF"; FAILURES=$((FAILURES + 1))
  fi
done

heading "per-session health"
SESSION_ID=$(load_session_id)
if [[ -z $SESSION_ID ]]; then
  printf '%s  no session id — run ./10-sessions.sh for a health example%s\n' "$DIM" "$OFF"
else
  request "GET" "/sessions/$SESSION_ID"
  expect 200
  for field in .health.socketConnected .health.consecutiveFailures .health.reconnectAttempts; do
    if (( HAS_JQ )) && printf '%s' "$LAST_BODY" | jq -e "$field != null" >/dev/null 2>&1; then
      printf '%s  ✓ %s present%s\n' "$GREEN" "$field" "$OFF"
    elif (( HAS_JQ )); then
      printf '%s  ✗ %s missing%s\n' "$RED" "$field" "$OFF"; FAILURES=$((FAILURES + 1))
    fi
  done
fi

printf '\n%sTracing one failed message end to end:%s\n' "$DIM" "$OFF"
printf '%s  curl -s -H "Authorization: Bearer $CONSOLE_API_KEY" %s/metrics \\%s\n' "$DIM" "$BASE_URL" "$OFF"
printf '%s    | grep baileys_send_failures_total%s\n' "$DIM" "$OFF"
printf '%s  docker compose logs app | grep '"'"'"idempotencyKey":"<key>"'"'"'%s\n' "$DIM" "$OFF"

finish
