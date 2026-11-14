# Native-shape regression fixtures

These are hand-authored, synthetic records using field layouts observed in local Codex desktop and Claude Code JSONL files on 2026-11-13. All identifiers, timestamps, paths, prompts and results are replaced examples; no private transcript content is included. They complement the original minimal fixtures.

The corpus covers Codex tool exit envelopes, spawn task paths (with separate ID-form unit coverage), agent messages, communication metadata and compaction; Claude native titles, spawn-result metadata with object, string and array shapes, cumulative reported costs/durations, hook failures, API errors and compaction. Native formats vary by client and version. The corpus records this observation date rather than claiming one universal schema or native end-to-end coverage.

Tests require exact semantic outcomes: one communication message plus separate metadata, failure evidence surfaced, unknown outcomes preserved, child IDs retained, compaction not replayed, and content absent without opt-in. Aggregate unsupported percentages alone cannot detect misinterpreted supported records.

Regression tests also cover oversized compaction headers with explicit partial provenance, encrypted block omission, script completion versus nested process exits, and degraded startup inventory. Browser tests assert the rendered outcome, scoped child navigation, failed page recovery, keyboard focus and notice persistence across polling.
