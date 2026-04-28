#!/usr/bin/env bash
set -euo pipefail

total_steps=2
if [[ "${RUN_RACE:-0}" == "1" ]]; then
  total_steps=$((total_steps + 1))
fi

current_step=1

echo "[${current_step}/${total_steps}] Running default test suite"
go test ./...
current_step=$((current_step + 1))

echo "[${current_step}/${total_steps}] Running integration test suite (build tag: integration)"
go test -tags=integration ./...
current_step=$((current_step + 1))

if [[ "${RUN_RACE:-0}" == "1" ]]; then
  echo "[${current_step}/${total_steps}] Running race detector suite"
  go test -race ./...
fi

echo "All automated test suites completed."
