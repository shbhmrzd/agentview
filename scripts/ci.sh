#!/bin/sh
# Portable CI entry point. No model calls, credentials, or live agent stores.
set -eu
cd "$(dirname "$0")/.."
mkdir -p test-results bin

printf '%s\n' 'Checking Go formatting'
unformatted=$(find cmd internal tools tests -name '*.go' -type f -exec gofmt -l {} +)
if [ -n "$unformatted" ]; then
  printf 'Unformatted Go files:\n%s\n' "$unformatted" >&2
  exit 1
fi
go version > test-results/toolchain.txt
go env GOOS GOARCH CGO_ENABLED >> test-results/toolchain.txt
printf '%s\n' 'Running go vet'
go vet -tags integration ./...

printf '%s\n' 'Running unit and CLI tests with race instrumentation'
if ! AGENTVIEW_QA_RACE=1 go test -race -count=1 -timeout 120s -coverprofile=test-results/coverage.out -covermode=atomic -json ./... > test-results/unit.jsonl; then
  cat test-results/unit.jsonl
  exit 1
fi
go tool cover -func=test-results/coverage.out > test-results/coverage.txt
printf '%s\n' 'Running browser view-model tests'
node --test tests/web/*.test.mjs > test-results/web-tests.txt
printf '%s\n' 'Running process integration tests with race instrumentation'
if ! AGENTVIEW_QA_RACE=1 go test -race -count=1 -tags integration -timeout 120s -json ./tests/integration > test-results/integration.jsonl; then
  cat test-results/integration.jsonl
  exit 1
fi
printf '%s\n' 'Running bounded parser fuzzing'
if ! go test ./internal/observe -run '^$' -fuzz FuzzNormalize -fuzztime "${FUZZ_TIME:-10s}" -parallel 2 > test-results/fuzz.txt 2>&1; then
  cat test-results/fuzz.txt
  exit 1
fi
printf '%s\n' 'Running stateful ownership fuzzing'
if ! go test ./internal/observe -run '^$' -fuzz '^FuzzCompactionOwnership$' -fuzztime "${FUZZ_TIME:-10s}" -parallel 2 > test-results/fuzz-ownership.txt 2>&1; then
  cat test-results/fuzz-ownership.txt
  exit 1
fi
printf '%s\n' 'Building local binary'
go build -trimpath -o bin/agentview ./cmd/agentview
printf '%s\n' 'CI passed. Reports: test-results/{unit,integration}.jsonl, coverage.txt, fuzz.txt'
