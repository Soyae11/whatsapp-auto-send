#!/usr/bin/env bash
# Session lifecycle: create, list, detail, QR. Does not connect a socket — see 11-pair.sh.
source "$(dirname "${BASH_SOURCE[0]}")/_common.sh"

heading "create a session"
request POST /sessions '{"label":"curl smoke test"}'
expect 201
expect_json_field .status new
expect_json_field .hasQr false

SESSION_ID=$(printf '%s' "$LAST_BODY" | (jq -r .id 2>/dev/null || true))
if [[ -z ${SESSION_ID:-} || $SESSION_ID == null ]]; then
  printf '%s  cannot continue without a session id (is jq installed?)%s\n' "$RED" "$OFF"
  finish
fi
save_session_id "$SESSION_ID"
printf '%s  session id: %s (saved for the later scripts)%s\n' "$DIM" "$SESSION_ID" "$OFF"

heading "reject a session with no label"
request POST /sessions '{}'
expect 400
expect_json_field .error_code invalid_payload
expect_json_field .retryable false

heading "list sessions"
request GET /sessions
expect 200

heading "session detail"
request "GET" "/sessions/$SESSION_ID"
expect 200
expect_json_field .id "$SESSION_ID"

heading "unknown session"
request GET /sessions/00000000-0000-0000-0000-000000000000
expect 404
expect_json_field .error_code session_not_found

heading "current QR (none until the socket connects)"
request "GET" "/sessions/$SESSION_ID/qr"
expect 200
expect_json_field .qr null

heading "reject a national-format number when pairing"
request "POST" "/sessions/$SESSION_ID/pair" '{"phoneNumber":"081234567890"}'
expect 400
expect_json_field .error_code invalid_payload

finish
