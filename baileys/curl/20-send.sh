#!/usr/bin/env bash
# The send endpoint and its error contract.
#
#   SESSION_ID=<uuid> TO=6287713848500 ./20-send.sh
#
# TO is required: this really does send a WhatsApp message. Without a connected session the
# script still runs and asserts the contract errors, which is worth doing on its own.
source "$(dirname "${BASH_SOURCE[0]}")/_common.sh"

SESSION_ID=$(load_session_id)
[[ -n $SESSION_ID ]] || skip "no session id — run ./10-sessions.sh first or set SESSION_ID"

SEND="/sessions/$SESSION_ID/send"

heading "reject a payload with no idempotency key"
request_send POST "$SEND" '{"to":"6287713848500","type":"text","text":"hi"}'
expect 400
expect_json_field .error_code invalid_payload
expect_json_field .retryable false

heading "reject an unsupported message type"
request_send POST "$SEND" '{"idempotencyKey":"curl-bad-type","to":"6287713848500","type":"image","text":"hi"}'
expect 400

heading "reject a national-format number"
request_send POST "$SEND" '{"idempotencyKey":"curl-bad-number","to":"081234567890","type":"text","text":"hi"}'
expect 400
expect_json_field .error_code invalid_payload

if [[ -z ${TO:-} ]]; then
  printf '\n%sTO is not set, so no message was sent.%s\n' "$DIM" "$OFF"
  printf '%sRerun with TO=6287713848500 against a connected session.%s\n' "$DIM" "$OFF"
  finish
fi

# One key per run, so repeated runs actually send rather than deduplicating forever.
KEY="curl-send-$(date +%s)"
BODY="{\"idempotencyKey\":\"$KEY\",\"to\":\"$TO\",\"type\":\"text\",\"text\":\"Phase 2 smoke test\"}"

heading "send a message"
request_send POST "$SEND" "$BODY"
if [[ $LAST_STATUS == 200 ]]; then
  expect 200
  expect_json_field .status sent
  expect_json_field .deduplicated false

  heading "same idempotency key again — must not send twice"
  request_send POST "$SEND" "$BODY"
  expect 200
  expect_json_field .deduplicated true
else
  # Not a script failure: an unconnected session is exactly what the contract describes.
  printf '%s  session is not sendable; checking the error contract instead%s\n' "$DIM" "$OFF"
  expect_json_field .retryable true
  printf '%s  error_code above should be session_not_connected or session_logged_out%s\n' "$DIM" "$OFF"
  FAILURES=0
fi

finish
