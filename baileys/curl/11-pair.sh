#!/usr/bin/env bash
# Connects a session and requests a pairing code. Needs a real phone to finish.
#
#   SESSION_ID=<uuid> PHONE=6287713848500 ./11-pair.sh
#
# Unlike the other scripts this one has side effects on a real WhatsApp account, so it
# refuses to run without both variables rather than guessing.
source "$(dirname "${BASH_SOURCE[0]}")/_common.sh"

# Opening a socket reaches WhatsApp's servers for real, so this one never runs by accident:
# it needs a phone number spelled out.
[[ -n ${PHONE:-} ]] || skip "set PHONE=6287713848500 to connect and request a pairing code"

SESSION_ID=$(load_session_id)
[[ -n $SESSION_ID ]] || skip "no session id — run ./10-sessions.sh first or set SESSION_ID"

heading "connect the socket"
request "POST" "/sessions/$SESSION_ID/connect"
expect 200

heading "request a pairing code for $PHONE"
request "POST" "/sessions/$SESSION_ID/pair" "{\"phoneNumber\":\"$PHONE\"}"
expect 200

if (( HAS_JQ )); then
  CODE=$(printf '%s' "$LAST_BODY" | jq -r .pairingCode)
  printf '\n  ┌────────────────────────┐\n'
  printf '  │  pairing code: %s  │\n' "$CODE"
  printf '  └────────────────────────┘\n\n'
fi
printf '%s  Enter it on the phone that owns +%s:%s\n' "$DIM" "$PHONE" "$OFF"
printf '%s    Linked devices → Link a device → Link with phone number instead%s\n' "$DIM" "$OFF"
printf '%s  Then poll /sessions/%s until status is "connected".%s\n' "$DIM" "$SESSION_ID" "$OFF"

finish
