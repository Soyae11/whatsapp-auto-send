#!/usr/bin/env bash
# The SSE QR stream. Reads frames for a few seconds and reports what arrived.
#
#   SESSION_ID=<uuid> ./30-qr-stream.sh
#
# The stream does not start a socket — that would make a page refresh a side effect. Call
# POST /sessions/:id/connect first (or ./11-pair.sh) to see QR frames rather than just the
# opening status frame.
source "$(dirname "${BASH_SOURCE[0]}")/_common.sh"

SESSION_ID=$(load_session_id)
[[ -n $SESSION_ID ]] || skip "no session id — run ./10-sessions.sh first or set SESSION_ID"

STREAM="$BASE_URL/sessions/$SESSION_ID/qr/stream"
SECONDS_TO_READ=${SECONDS_TO_READ:-6}

heading "stream headers"
HEADERS=$(curl -sS -D - -o /dev/null --max-time 3 -H "Authorization: Bearer $MANAGE_KEY" "$STREAM" 2>/dev/null || true)
printf '%s\n' "$HEADERS" | head -6

LAST_STATUS=$(printf '%s' "$HEADERS" | head -1 | grep -o '[0-9]\{3\}' | head -1)
expect 200 "stream opened"

if printf '%s' "$HEADERS" | grep -qi 'content-type: text/event-stream'; then
  printf '%s  ✓ content-type is text/event-stream%s\n' "$GREEN" "$OFF"
else
  printf '%s  ✗ wrong content-type%s\n' "$RED" "$OFF"; FAILURES=$((FAILURES + 1))
fi
if printf '%s' "$HEADERS" | grep -qi 'x-accel-buffering: no'; then
  printf '%s  ✓ proxy buffering disabled%s\n' "$GREEN" "$OFF"
else
  printf '%s  ✗ x-accel-buffering missing — a proxy would hold the whole stream%s\n' "$RED" "$OFF"
  FAILURES=$((FAILURES + 1))
fi

heading "rejects an unauthenticated stream"
LAST_STATUS=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 3 "$STREAM")
printf '%s\n' "$LAST_STATUS"
expect 401

heading "frames received in ${SECONDS_TO_READ}s"
FRAMES=$(curl -sS --no-buffer --max-time "$SECONDS_TO_READ" \
  -H "Authorization: Bearer $MANAGE_KEY" "$STREAM" 2>/dev/null || true)

if [[ -z $FRAMES ]]; then
  printf '%s  no frames — is the session connecting? try:%s\n' "$RED" "$OFF"
  printf '%s    curl -X POST -H "Authorization: Bearer \$CONSOLE_API_KEY" %s/sessions/%s/connect%s\n' \
    "$DIM" "$BASE_URL" "$SESSION_ID" "$OFF"
  FAILURES=$((FAILURES + 1))
else
  printf '%s\n' "$FRAMES" | sed 's/^/  /'
  if printf '%s' "$FRAMES" | grep -q '^event: '; then
    printf '%s  ✓ well-formed SSE frames%s\n' "$GREEN" "$OFF"
  else
    printf '%s  ✗ no event: lines in the stream%s\n' "$RED" "$OFF"; FAILURES=$((FAILURES + 1))
  fi
fi

printf '\n%sTo watch it live while pairing:%s\n' "$DIM" "$OFF"
printf '%s  curl -N -H "Authorization: Bearer \$CONSOLE_API_KEY" %s%s\n' "$DIM" "$STREAM" "$OFF"

finish
