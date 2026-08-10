#!/usr/bin/env bash
# Runs every numbered script in order and reports a single pass/fail.
# Scripts that need extra config (a real phone number) report themselves as skipped.
set -uo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXIT_SKIPPED=78
failed=0
skipped=0
passed=0

for script in "$DIR"/[0-9]*.sh; do
  printf '\n\033[1m--- %s ---\033[0m\n' "$(basename "$script")"
  bash "$script"
  case $? in
    0) passed=$((passed + 1)) ;;
    $EXIT_SKIPPED) skipped=$((skipped + 1)) ;;
    *) failed=$((failed + 1)) ;;
  esac
done

printf '\n'
if (( failed == 0 )); then
  printf '\033[32m%d script(s) passed\033[0m' "$passed"
else
  printf '\033[31m%d script(s) failed\033[0m, %d passed' "$failed" "$passed"
fi
(( skipped > 0 )) && printf ', \033[2m%d skipped\033[0m' "$skipped"
printf '\n'
exit $(( failed > 0 ? 1 : 0 ))
