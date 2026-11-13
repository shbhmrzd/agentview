# Process integration tests

These opt-in tests build the real AgentView binary once, start separate deterministic fixture-writer processes, and observe their files through AgentView's actual CLI. They do not start native agent CLIs, call a model, use account credentials, or read existing private histories. Passing these tests is process/fixture integration evidence, not native-agent compatibility evidence.

Run from the repository root:

```sh
go test -tags=integration ./tests/integration -count=1 -v
```

For race instrumentation in the test runner, fixture writers, and built AgentView subprocess:

```sh
AGENTVIEW_QA_RACE=1 go test -race -tags=integration ./tests/integration -count=1 -v
```

The suite runs on macOS and Linux. Its build tag keeps it out of default package tests. It uses the caller's Go cache; restricted environments can set `GOCACHE` to a writable temporary directory. No external Go modules are required.

Append-log coverage includes Claude, Codex, and Copilot synthetic record shapes, native/session identifiers, tool call correlation, metadata-only and content-enabled output, initial history and subsequent activity, incomplete records, rotation while a partial record exists, missing-source recovery, source bytes and permissions, bounded observer shutdown, and a still-running producer accepting writes after the observer exits.

Rovo replacement-snapshot coverage verifies full projection boundaries and byte revision hashes, metadata/content policy, excluded reasoning, revision-local part positions without invented native message IDs, in-place rebuilds, partial and unsupported snapshot recovery, same-byte atomic replacement after disappearance, pruning, native session identity changes, and producer survival. Replayed snapshot contents represent the current document, not newly executed actions or semantic incremental deduplication. A transient `snapshot.unstable` diagnostic is allowed when a replacement races a read; unexpected projections and schema errors fail the test.

The fixture-writer protocol acknowledges each operation only after syncing its file and returns its PID, source length, and SHA-256. Tests compare those results against bytes independently accumulated from commanded writes. Viewer output and source-unavailable/available events form additional handshakes. Deadlines bound every wait; tests do not use sleep-only readiness assertions.

For the start-before-writes case, the viewer process is started before the first source records are written. The CLI currently has no empty-source readiness event, so this does not claim that the viewer completed its initial open before the first append. The first normalized event establishes the observation handshake before further activity is generated.

Native CLI smoke tests are a separate, explicitly enabled layer maintained by the main project. They can involve account access and model usage and must identify the actual CLI versions and execution outcomes.

Directory tests run independent Claude/Codex fixture writers beneath a shared root. They attach to existing history, create new nested directories and sessions, continue both old and new writers, and verify identity isolation, missing/replaced-source recovery, bounded shutdown, source integrity, and metadata-only historical inspection. `directory.ready` and per-source `source.ready` events provide explicit startup barriers. These are process tests against synthetic writers, not native model runs.

Dashboard process cases start the actual `serve` command on an ephemeral loopback port, use its per-launch token, verify old history versus new activity, discover a child agent written by an independent process, and prove both writers continue after the observer exits. These are deterministic HTTP integration tests; browser interaction is additionally checked manually through the local dashboard. No native model call is part of CI.
