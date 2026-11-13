# AgentView

A Go experiment for inspecting existing agent session records through provider adapters, with explicit source provenance and uncertainty. It reads a selected file and does not launch, resume, interrupt, or configure agents.

**Status: CLI prototype.** AgentView inspects and follows explicitly selected session files. Automatic discovery, a dashboard, search, and persistent history are not implemented.

## Run the experiment

Use Go on macOS or Linux. The code uses Go 1.22 language features; local validation uses the installed Go 1.22.12 toolchain. That old toolchain is not a public-release support recommendation: supported-Go validation is a release gate. There are no third-party dependencies in this experiment.

```sh
go build -o bin/agentview ./cmd/agentview
./bin/agentview inspect --agent claude --file testdata/claude.synthetic.jsonl
./bin/agentview inspect --agent codex --file testdata/codex.synthetic.jsonl
./bin/agentview inspect --agent copilot --file testdata/copilot.synthetic.jsonl
./bin/agentview inspect --agent rovo --file testdata/rovo.synthetic.json
./bin/agentview adapters
```

All demo inputs are [original synthetic fixtures](testdata/README.md). No agent is started and no model is called. The output is JSONL metadata; prompts, responses, and tool payloads are omitted by default.

| Adapter | Selected input | Validation level |
|---|---|---|
| `claude` | Native transcript JSONL | Fixture/process tests and native seed/resume checks |
| `codex` | Native rollout JSONL | Fixture/process tests and native seed/resume checks |
| `copilot` | Copilot CLI `events.jsonl` | Source-backed fixtures/process tests; native access blocked by account policy |
| `rovo` | Rovo Dev CLI `session_context.json` | Native request-history read plus source-backed fixtures/process tests; native model response failed |

Copilot and Rovo are experimental adapters. This does not advertise Copilot IDE/cloud or Rovo cloud agent support. See [provider format evidence](docs/provider-format-evidence.md).

To inspect a real source, explicitly supply its path:

```sh
./bin/agentview inspect --agent claude --file /absolute/path/to/transcript.jsonl
./bin/agentview follow --agent codex --file /absolute/path/to/rollout.jsonl
```

Add `--include-content` only when you want user-facing transcript text and tool arguments/results in the output. Recognized reasoning/thinking records remain omitted. Opting in is **not redaction**; output may contain sensitive data. Redirected output is your own copy and is not managed by AgentView. No history is persisted by AgentView yet.

## Command behavior

`inspect` reads through the byte size at the first successful open. It reports an incomplete final line without parsing it and exits. It does not chase appends past that boundary. A detected source replacement or rewrite during inspection fails the command; use a stable copy if needed. This is a bounded inspection, not an atomic filesystem snapshot.

`follow` reads existing history and then checks for appends every second. Use `--poll 500ms` to change the interval (minimum 100 ms). Ctrl-C stops AgentView only. On termination, the CLI attempts graceful cancellation and uses a 500 ms fallback to exit its own process if output is blocked. Pending output can be dropped and the final JSONL line can be partial; there is no durable store yet. After the first successful read, missing/inaccessible sources produce a diagnostic and are retried. On detected replacement, truncation, or rewrite, the reader reports a new generation and starts from the beginning; repeated or missing history is possible and explicitly reported.

Rovo uses replacement snapshots rather than appended records. `follow --agent rovo` reads a bounded complete JSON document and emits a `snapshot.updated` boundary followed by its current contents. Replace the prior snapshot projection; do not append those contents as new execution. Every event carries `capture_mode: snapshot_observation` and a source revision; part positions are revision-local indices, not byte offsets or invented native message IDs. Identical bytes are suppressed even after inode replacement. Invalid JSON or unsupported known shapes retain the last accepted revision. A revision change may represent pruning or an empty history, not new activity.

**Rovo snapshot coverage is limited to the first 254 parts, including ignored system/thinking parts**, followed by an omission diagnostic if more exist. Long histories may omit their newest messages. The byte limit applies to the entire snapshot; both file formats default to 1 MiB. The reader checks file identity/size/modification time around a snapshot read; this is not a transactional filesystem snapshot or a guarantee to observe every intermediate save.

Input is limited to regular files. Final-component symlinks are rejected; trusted ancestor directory symlinks can resolve normally. Sources are opened read-only and without blocking on a FIFO. Maximum record size defaults to 1 MiB; `--max-record-bytes` accepts 1 byte through 16 MiB. Oversized input is discarded until its newline with a diagnostic. Each batch reads at most 4 MiB and returns at most 1,024 records. At most 255 message blocks per record are projected, followed by an explicit omission diagnostic if needed.

Exit codes: 0 means the inspection completed or following was cancelled; 1 means an I/O failure; 2 means invalid arguments. A zero exit code does not mean all records were understood: inspect `record.invalid`, `record.unsupported`, `record.oversized`, and other diagnostic events.

## What the output means

- `source` identifies the selected input and attachment generation.
- JSONL sources use byte offsets; snapshots use a revision and optional part index. `adapter_version` identifies the decoding contract.
- `session_key` namespaces native session IDs by provider and `--scope` (default `local`). Use distinct stable scopes when combining different hosts/accounts. Source paths identify selected inputs, not canonical session identity. Scoped occurrence IDs remain attachment-local and can differ from pre-adapter prototype IDs.
- `occurrence_id` identifies an occurrence in this attachment. It is not a durable cursor or cross-export deduplication promise.
- `occurred_at` is the source timestamp when valid; `time_status` distinguishes a valid source time, an absent timestamp, and an invalid timestamp. `observed_at` is when AgentView projected the record. Late import does not make historical activity current.
- `execution_state` stays `unknown`. File changes, a recorded turn ending, or an assistant saying “done” cannot establish task success or current process state.
- `tool.requested` records an invocation request; it does not prove execution began. `tool.result` retains structured error information only when the source supplies it.
- Claude parent IDs identify conversation records, not parent agents. Codex thread IDs and optional group session IDs are separate fields.
- Copilot preserves `checkpoint_replay` and `native_agent_id` when supplied. These are source facts; a subagent tree and current execution state are not inferred from them.
- Content-free diagnostics describe malformed, unsupported, skipped, and changing input without printing raw record contents.

The timeline follows physical record order. Reconstructing compacted/branched conversations, tool-pair durations, current approval state, native subagent lineage, search, a web UI, SQLite history, persistent checkpoints, retention, and managed exports are future work. Native smoke tests have exercised newly created Claude Code 2.1.316 and Codex CLI 0.149.0 JSONL sessions. Those checks covered a seed response and a second response after resuming the same disposable session while AgentView followed its file; they do not establish compatibility with other versions or storage formats. Some Codex histories use other storage formats and are not covered by this JSONL reader.

The reader checks file identity plus bounded prefix and pre-cursor content anchors. It detects common same-inode truncate/regrow cases, but cannot detect every edit outside those windows or changes that occur and are reverted between reads. It cannot recover source records deleted before observation. There is no complete-history or exactly-once claim.

## Development and review

See [CONTRIBUTING.md](CONTRIBUTING.md) for contribution scope and safe bug reports.

```sh
make ci           # format, vet, race tests, process integration, fuzz, build
make test         # unit + CLI regression tests
make integration  # actual binary with independent synthetic writer processes
make fuzz         # bounded parser fuzzing
```

Routine tests use isolated fixtures, make no model calls, and do not touch live agent stores. Go's standard `testing` framework supplies shared adapter conformance tests, 11 subprocess integration scenarios, race detection, coverage, and fuzzing without additional dependencies. Reports go to ignored `test-results/`; `make ci` is the future GitHub workflow's entry point. GitHub Actions is not configured yet. In a restricted workspace, use a writable cache, for example `GOCACHE=/private/tmp/agentview-go-cache make ci`.

Native compatibility is a separate opt-in check:

```sh
make native-claude
make native-codex
# If the installed Codex default model is unavailable for your login:
go run ./tools/native-smoke --agent codex --agentview bin/agentview --model <available-model>
```

These commands start two model-backed turns in a new disposable session using the installed CLI's normal login and persistence. The test harness owns those child processes; AgentView still only reads an explicitly selected file. The harness waits for the initial assistant response through AgentView, resumes that exact session, then requires the new assistant response at a later source offset. It also checks metadata-only output and source integrity. It never uses `--last` or copies credentials. Claude has a $0.50 estimated budget per invocation; both providers have a 90-second default deadline per turn. Logs can contain native metadata, so artifacts are private and must not be published unreviewed. Native test sessions remain in the CLI's store; no shared history is deleted. See the [testing guide](docs/testing.md).

See the [adapter extension guide](docs/adapters.md), [architecture](docs/architecture.md), and [testing guide](docs/testing.md).

AgentView is licensed under the [MIT License](LICENSE). Copyright 2026 Shubham Raizada. Third-party product names identify integrations and do not imply vendor affiliation or endorsement.
