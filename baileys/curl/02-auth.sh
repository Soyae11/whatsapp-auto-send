#!/usr/bin/env bash
# Bearer-token enforcement on everything except /health.
# Probed against a route that will never exist: a 401 proves the auth hook runs before
# routing, and a 404 proves a valid token gets past it.
source "$(dirname "${BASH_SOURCE[0]}")/_common.sh"

heading "no Authorization header"
request_noauth GET /no-such-route
expect 401
expect_json_field .error_code unauthorized
expect_json_field .retryable false

heading "wrong token"
request_badauth GET /no-such-route
expect 401 "rejected before the route is resolved"

heading "wrong scheme carrying the right secret"
printf '%sGET %s/no-such-route (Basic instead of Bearer)%s\n' "$DIM" "$BASE_URL" "$OFF"
LAST_STATUS=$(curl -sS -o /dev/null -w '%{http_code}' \
  -H "Authorization: Basic $MANAGE_KEY" "$BASE_URL/no-such-route")
printf '%s\n' "$LAST_STATUS"
expect 401 "only Bearer is accepted"

heading "valid token, unknown route"
request GET /no-such-route
expect 404 "auth passed, then routing failed"
expect_json_field .error_code not_found

heading "auth applies to paths with a query string"
request_noauth GET '/no-such-route?limit=1'
expect 401

# Authentication says who you are; these two say what that buys you. Both refusals are the
# whole reason the single INTERNAL_API_KEY was split in two.
if [[ -n ${CONSOLE_API_KEY:-} ]]; then
  heading "the console credential cannot send"
  request POST /sessions/00000000-0000-0000-0000-000000000000/send \
    '{"idempotencyKey":"curl-scope","to":"6287713848500","type":"text","text":"hi"}'
  expect 403 "sending is the dispatcher's job, and its pacing is not optional"
  expect_json_field .error_code forbidden

  heading "the dispatcher credential cannot manage a session"
  request_send POST /sessions/00000000-0000-0000-0000-000000000000/logout
  expect 403 "the lifecycle belongs to the console"
  expect_json_field .error_code forbidden
fi

finish
