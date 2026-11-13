# Provider adapter contract

Implemented 13 November 2026. Adapters are compiled-in Go implementations. There is no public plugin ABI, remote protocol, automatic discovery, or process-control API in this milestone.

## Boundaries

`internal/observe/adapters.go` registers each descriptor, decoder factory, and optional snapshot validator. `agentview adapters` prints the registry's capabilities. Adding a provider does not require provider-specific CLI branches.

Acquisition and interpretation are separate:

- `SourceReader` supplies bounded source records, diagnostics, and positions. JSONL uses `Reader`; replacement documents use `SnapshotReader`.
- Each decoder implements `Decode`, `Context`, and `Reset` and owns native session context for one attachment. A new normalizer gets independent state.
- `Normalizer` adds shared provenance, observation time, adapter version, capture mode, scoped session keys, and occurrence identities.
- The CLI selects a registered adapter and reader, then follows the same observation loop for every provider. It owns no native agent process.

The current providers are Claude transcript JSONL, Codex rollout JSONL, Copilot CLI disk event JSONL, and Rovo Dev CLI session-context snapshots. Source evidence is tracked in [provider-format-evidence.md](provider-format-evidence.md) and the current validation levels are summarized in the [README](../README.md).

## Identity and provenance

Native `session_id` remains unmodified. `session_key` is a SHA-256 namespace of provider, selected scope, and native session ID. The default scope is `local`; callers combining hosts/accounts must choose distinct stable `--scope` values. A missing native identity stays missing. Session identity is independent of source-file identity; changing a selected file need not imply a different native session.

Occurrence IDs include provider, scope, source path, generation, record position, projected-event index, raw record digest, and event kind. Snapshots also include the revision. These IDs identify observations, not semantic deduplication or durable restart checkpoints. There is no persistent ID migration contract yet.

`source.kind` discriminates positions. JSONL carries byte offsets. Snapshots carry a full-document revision hash and revision-local part index; snapshot JSON omits byte offsets. Native timestamps and observation timestamps remain distinct. File evidence always leaves `execution_state` unknown.

Copilot's envelope event ID, message ID, optional native agent ID, and optional checkpoint-replay flag remain distinct. A checkpointed physical row can replay earlier activity. Neither an agent ID nor a record-parent ID automatically establishes a subagent tree.

## Snapshot semantics

A valid changed snapshot emits `snapshot.updated`, followed by the supported projection of that revision. Consumers replace the previous snapshot projection rather than append the contents as newly executed actions. An empty valid history explicitly clears the projected history. Pruning is a replacement, not evidence that previously executed tools were undone.

The reader suppresses identical bytes across repeated reads and inode replacements. It checks bounded reads, regular-file policy, metadata stability, JSON validity, and the registered provider's schema validator before advancing the accepted revision. Invalid or unsupported replacements keep the last accepted revision; diagnostics carry no raw contents. Recovery of the same accepted bytes does not replay the projection. Unknown future part kinds remain explicit unsupported records; invalid shapes of known supported parts reject the snapshot.

Memory/input size is bounded by `--max-record-bytes` (default 1 MiB, maximum 16 MiB). Output is bounded to a boundary, the first 254 parts, and an omission diagnostic when necessary. Ignored system/thinking parts count toward this bound. **This is partial prefix coverage: long histories may omit their newest messages.** Paging/recent-history selection is future work. The reader's identity/size/modification-time checks are not a filesystem transaction and cannot detect every concurrent rewrite or intermediate save.

## Extending the registry

1. Establish a primary-source format contract, a pinned schema/version, and explicitly authorized native samples where feasible. CLI transport output is not automatically its persisted history format.
2. Choose an existing source reader or add a separately tested bounded reader. A new snapshot provider supplies validation before revision acceptance.
3. Implement a decoder in its own file and register its descriptor/factory. Declare partial/unknown/unavailable capabilities accurately; do not manufacture process state, native IDs, or subagent relationships.
4. Add original synthetic fixtures, table-driven conformance cases, and process tests against the actual built CLI. Include metadata-only behavior, reasoning exclusion, unknown fields/types, source replacement/recovery, and observer exit while the producer continues.
5. Run `make ci`. Perform a separate native history/live test before upgrading the compatibility claim. Record access, authentication, storage, or version blockers as non-passes.

The existing Go interface and versioned event envelope are sufficient for in-process adapters. If remote collectors or independently distributed adapters become necessary, a transport can be specified around the observation contract then. Observation and any future agent-control capability must remain separate interfaces.
