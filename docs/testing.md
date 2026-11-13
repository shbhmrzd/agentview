# Testing AgentView

Run the deterministic suite from the repository root:

```sh
make ci
```

This invokes `scripts/ci.sh`: formatting checks, `go vet`, race-instrumented unit and CLI tests, process integration tests, bounded parser fuzzing, and a build. It requires Go (the module selects Go 1.26.10) and Node.js 20+, and makes no model calls. In restricted environments, use a writable cache such as `GOCACHE=/private/tmp/agentview-go-cache make ci`.

## Test layers

- Unit and CLI regression tests check decoding, bounds, source changes, diagnostics, arguments, and content policy. Shared adapter conformance tests cover scoped identities, capabilities, source positions, and snapshot limits.
- Seventeen process integration scenarios exercise the built CLI against independent synthetic writers: nine JSONL cases across Claude, Codex, and Copilot, two Rovo snapshot cases, four Claude/Codex directory cases, and two authenticated dashboard HTTP cases. They cover append/replacement, incomplete input, recovery, source integrity, and the writer continuing after AgentView exits. Directory cases additionally cover new nested directories, simultaneous new and resumed sessions, startup-history policy, per-source context, and finite reinspection. Unit tests cover frozen discovery boundaries, incomplete-tail memory, symlink replacement, fair scheduling, visible coverage limits, and compact terminal output. See the [integration suite](../tests/integration/README.md).
- Browser view-model tests use Node’s built-in test runner to verify time/provider/text filters, activity expiry and disconnection, tour-dismissal storage, safe wrapper formatting, bounded relationship traversal, ambiguous-recipient handling, and coordination filtering. Dashboard Go tests cover API authorization, history limits, source integrity, snapshot accounting, source-generation isolation, and the distinction between history/file discovery and newly appended conversation activity. Process tests verify that distinction for both Claude and Codex through the authenticated HTTP API.
- Theme tests cover startup choice, persistence, operating-system fallback, cross-tab updates, blocked storage, palette completeness, and common text contrast pairs. Tree tests cover provider isolation, missing/ambiguous parents, cycles, response limits, inherited-message exclusion, UTF-8 excerpt limits, generation resets, and metadata-only privacy. Both provider process cases also fetch the family endpoint.
- Parser fuzzing runs for ten seconds by default; set `FUZZ_TIME` to change the sample duration. This is bounded sampling, not proof against all malformed input.

Use `make test`, `make integration`, or `make fuzz` to run a layer independently. Reports go to ignored `test-results/`. Statement coverage excludes separately built subprocesses and is not a completeness score. Fixtures are [original synthetic records](../testdata/README.md).

## Optional native smoke tests

```sh
make native-claude
make native-codex
# Select a model available to the installed Codex login when needed:
go run ./tools/native-smoke --agent codex --agentview bin/agentview --model <available-model>
```

These commands start two model-backed turns in a new disposable session using the installed CLI's normal login and persistence. They may incur usage. They are never run by `make ci`. Optional `--timeout` and `--output` flags configure the harness; the default deadline is 90 seconds per turn. Claude uses a $0.50 estimated budget per invocation, which is not a billing guarantee.

The harness identifies only its newly created session, observes its seed response, then resumes that exact session while the same AgentView follower runs. A pass requires the second assistant marker at a later source offset, expected native identity, metadata-only behavior, and unchanged source bytes. Prompt echoes do not count. The harness owns its test child processes; the product still only reads the chosen file.

Logs can contain native metadata and remain private. Native test sessions remain in the provider's store; the harness does not delete shared history or copy credentials. Missing authentication, unsupported storage, policy blocks, and native failures are not compatibility passes. The [README](../README.md) distinguishes native-tested and fixture-tested adapters.

GitHub Actions is not configured yet. `make ci` is the existing entry point for a future workflow; native calls require a separate opt-in environment.
