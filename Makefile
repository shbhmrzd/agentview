.PHONY: build test integration fuzz check ci native-codex native-claude demo

build:
	go build -o bin/agentview ./cmd/agentview

test:
	AGENTVIEW_QA_RACE=1 go test -race -count=1 ./...

integration:
	AGENTVIEW_QA_RACE=1 go test -race -count=1 -tags integration -timeout 120s ./tests/integration

fuzz:
	go test ./internal/observe -run '^$$' -fuzz FuzzNormalize -fuzztime 10s -parallel 2

check: ci

ci:
	sh scripts/ci.sh

.PHONY: browser release
browser: build
	npm run test:e2e

release:
	sh scripts/release.sh $(if $(VERSION),$(VERSION),dev)

# Explicitly opt-in: these launch two turns using the installed CLI and its login.
native-codex: build
	go run ./tools/native-smoke --agent codex --agentview bin/agentview

native-claude: build
	go run ./tools/native-smoke --agent claude --agentview bin/agentview

demo:
	go run ./cmd/agentview inspect --agent claude --file testdata/claude.synthetic.jsonl
	go run ./cmd/agentview inspect --agent codex --file testdata/codex.synthetic.jsonl
	go run ./cmd/agentview inspect --agent copilot --file testdata/copilot.synthetic.jsonl
	go run ./cmd/agentview inspect --agent rovo --file testdata/rovo.synthetic.json
