# Directory observation

Directory mode is an ephemeral developer preview for Claude and Codex on macOS/Linux. Select one provider and one trusted local session root per invocation. Observation reads persisted files; it never owns native processes or establishes liveness.

## Modes

| Command | Initial files | Later discoveries |
|---|---|---|
| `follow --dir PATH --history none` (default) | Decode history for session context, suppress historical record output, then emit updates | Read from the beginning, then follow |
| `follow --dir PATH --history all` | Emit history, then follow | Read from the beginning, then follow |
| `inspect --dir PATH` | Capture the discovered set and each file's read boundary; process once | Not included after the initial scan |

Boundaries are per-file first successful opens during scanning, not one atomic timestamp for the whole tree. A record incomplete at its boundary is emitted when later completed. Timestamp skew does not discard newly appended records. Copying an old file into the tree produces newly discovered history, not proof of a newly created agent session.

`--history none` controls output, not source access: historical records are still decoded to initialize session identity. Initial processing can take time on large stores. Diagnostics and inventory remain available even when historical messages are suppressed. A delayed first open after an access failure establishes a later boundary and reports `source.baseline_delayed`.

There are no durable checkpoints. Restarting begins a fresh observation, and default new-activity mode suppresses records written while AgentView was stopped. History mode or a finite inspection can re-read retained source records. This is reprocessing, not a summarization or analysis engine.

## Discovery and limits

- Claude candidates are non-hidden `*.jsonl` files. Codex candidates are non-hidden `rollout-*.jsonl` files. Decoders report unsupported record shapes; filenames alone do not establish native session identity.
- Scans recurse into non-hidden directories. They skip discovered symlinks and special files. The explicitly configured root is resolved once; output uses the resolved path.
- Up to 10,000 source paths are retained per run, including temporarily missing sources. Each scan visits at most 100,000 entries and descends at most 64 levels. Limits produce `directory.limit`, not silent complete-coverage claims. Choose a narrower root when a limit is reached.
- Traversal reads directory entries in bounded pages. One scheduling round reads one bounded batch per eligible source. No goroutine or open file is retained for every session. Incomplete payloads are reread rather than buffered across idle directory sources.
- Scans use file identity, size, and modification time to schedule reads. They do not use file dates or modification time as an event cutoff. An edit preserving all scheduling metadata may remain undetected until a subsequent observable change; sampled reader anchors are not universal rewrite detection.
- Scans and output can delay observation under load. Files created and removed between scans, or records removed before reading, cannot be recovered. No latency or complete-history guarantee is made.
- Descendant components are checked for symlinks before reads. These are trusted local roots, not a sandbox against concurrent adversarial ancestor swaps. Root filesystem reads can still block on an unhealthy filesystem; bounded cancellation does not establish network-filesystem support.

Missing/unreadable sources report their own failure and retry during following. Other registered sources continue. Unreadable subtrees or scan limits report incomplete directory coverage. Inspection continues through readable sources but exits nonzero on source-access or discovery-coverage failures. Malformed/unsupported records remain diagnostics, consistent with file inspection.

## Output

`--format auto` shows compact text in a terminal and JSONL in pipes/files. `--format text` and `--format jsonl` select explicitly. `--include-content` enables supported message/tool text; reasoning remains excluded.

Text output summarizes startup, hides `record.ignored`, and reports each unsupported native type once (up to 32 distinct types, then a summary). It aggregates oversized historical records during new-activity initialization. It displays up to 500 characters of content per event with an explicit truncation marker and escapes terminal controls. Use JSONL to inspect full allowed content and every diagnostic; text is a presentation, not a complete export.

JSONL directory events include:

- `source.discovered`: a candidate file was registered, possibly before native identity is known.
- `directory.ready`: initial discovery finished; source initialization may still be in progress and coverage diagnostics may apply.
- `source.ready`: the reader reached its initial boundary, with native identity when available. Neither this nor file presence means the agent is running.
- `source.unavailable` / `source.available`: source access failed/recovered.
- `directory.limit`, `directory.unavailable`, `directory.recovered`: discovery coverage changed.

Decoded records include `observation_phase`: `history` for initial file records, `discovered_history` for the initial contents of later discoveries, `replacement_history` for a reset source's initial contents, and `update` for subsequent records. These classify reading relative to source boundaries, not execution time. Per-source order is preserved; interleaving sources does not establish a global causal order.

Sources and native sessions remain distinct. Multiple files can report the same native session key; this preview does not semantically deduplicate copied or moved transcripts. Replacement/truncation can repeat history and is explicitly diagnosed. No source records, native sessions, or provider settings are modified.
