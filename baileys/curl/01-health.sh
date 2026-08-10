#!/usr/bin/env bash
# GET /health — the one route that needs no bearer token.
source "$(dirname "${BASH_SOURCE[0]}")/_common.sh"

heading "health, authenticated"
request GET /health
expect 200 "Postgres is reachable"
expect_json_field .status ok
expect_json_field .db ok

heading "health needs no token"
request_noauth GET /health
expect 200 "public by design, so orchestrators can probe it"

printf '\n%sTo see the degraded path:%s\n' "$DIM" "$OFF"
printf '%s  docker compose stop postgres && %s && docker compose start postgres%s\n' \
  "$DIM" "$(basename "${BASH_SOURCE[0]}")" "$OFF"
printf '%s  expect 503 with {"status":"degraded","db":"down"} and no container restart%s\n' "$DIM" "$OFF"

finish
