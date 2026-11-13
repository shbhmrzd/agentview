# Local agent workspace

Run `agentview serve --include-content` and open the complete printed URL. The Go binary contains the browser assets; it needs no Node.js runtime, package installation, cloud account, or frontend build. Default roots cover Claude Code and Codex. Select a provider with `--agent`; use `--dir` with a specific provider to override its root. `--listen` accepts loopback IPs only and defaults to `127.0.0.1:7433`.

## Sessions and agents

The default **Conversations** list has one row per recorded conversation family: the main source, child agents, and related exports. Grouping uses provider-scoped session, group, and parent IDs. It never merges chats just because they share a project directory, a title, or fork provenance. Missing parents remain visible as incomplete families. A conversation's activity is the latest activity across its members, so an active child brings its older main conversation into the default 24-hour window. **All history** includes older conversations.

Row titles are literal excerpts from the first eligible recorded user request (up to 120 characters), available only with `--include-content`; known bootstrap and inherited records are skipped. These are not native chat titles or generated summaries. Without a request title, rows show a shortened conversation ID. The project name appears beneath the title. Search covers all members' request titles, IDs, agent names, project directories, and source paths. Searching for a child finds its owning conversation.

Use **List view → All sources** to inspect individual transcript files, including children and duplicate exports. Sidebar and activity counts use the current view's units; the note above the search shows the total conversation/source counts. A source is not necessarily a distinct agent or running process. Selecting a child trace keeps its conversation highlighted in the default list. Sources without recorded links remain separate until identifying metadata arrives.

To find a conversation you are using now, send a message in Claude Code or Codex, then choose **New activity (2 min)** or click the activity count above the workspace. A green **New activity** label means AgentView observed an appended message, tool, or turn record in the last two minutes. Startup history, newly discovered file history, replacement history, and diagnostic records do not trigger that label. **Recent file activity** means the source was recently modified or discovered without such an update. **No recent updates** can mean the agent is working silently, waiting, or stopped; file observation cannot distinguish these states. Disconnection and unreadable sources have their own labels. The two-minute window expires automatically and starts fresh when the observer restarts.

On your first visit, an optional tour explains the activity filter, session list, agent relationships, live view, payloads/results, and metrics. Choose **Skip tour**, press Escape, or finish to dismiss it; **Take a tour** reopens it at any time. Dismissal is stored locally for that browser and server origin (including its port); if browser storage is disabled it may appear again after reloading. The tour highlights the actual controls without changing your selection or adding sample sessions. Its payload step includes two expanded sample cards that you can collapse and reopen. **Payload guide** beside the trace filters opens that example directly, including in metadata-only mode. Sample commands are never executed.

## Agent tree

Choose **Agent tree** to view nested parent/child branches. Each card previews the first recorded user request and latest recorded assistant reply. Expand a branch to inspect its children, message excerpts and provenance; **Open trace** opens that source's history. Collapsing a branch hides its descendants. Activity labels retain their observation-only meaning.

The request excerpt is not a verified spawn instruction. Known inherited Codex records are excluded, but sources without an inheritance boundary may still contain copied context. The latest reply may be progress rather than a final return value. Missing messages are unavailable, not fabricated summaries. A group ID collects related sources but does not establish a parent edge; missing or ambiguous parents and cyclic links are identified explicitly.

Tree excerpts require `--include-content`, are limited to 1 KiB each, and reset on a source-generation change. A family response contains at most 100 sources and reports truncation. These excerpts are fetched only for the selected family, not included in every inventory poll.

## Appearance and theme extension

The **Theme** selector in the top bar offers **Light** and **Dark**. The first visit uses your operating system preference; choosing a mode persists it in this browser for this server origin. Without browser storage, the choice still works until the page is closed or reloaded. Theme choice applies before styles load and synchronizes between tabs.

Component colors use semantic CSS variables such as `--surface`, `--ink`, `--muted`, `--accent`, and `--line`. Both palettes live in `internal/dashboard/web/themes.css`; the bundled theme registry and preference handling live in `theme.js`. Future bundled themes can add a registry entry and a complete palette without changing component layouts. Custom theme import/editor support is not yet implemented. Keep additional theme CSS local, implement every existing token, and check text contrast. Automated tests validate palette completeness, common contrast pairs, persistence, and storage failures.

The agent selector follows explicit Codex group and parent-thread IDs. It reads parent/name/role/path metadata from `session_meta`, including `source.subagent.thread_spawn`. Claude subagent files beneath `<session>/subagents/` are related using that directory layout; the UI identifies this as layout-derived evidence. Parent message UUIDs are not treated as agent parents. Missing parents remain unknown; the dashboard does not read files outside configured roots to find them. Fork provenance is shown separately from parent-agent relationships.

This is partial relationship coverage. A file may contain inherited history, some providers use other storage formats, and tools may omit links. The UI does not claim that every displayed source is a distinct running process or that every child is currently active.

Codex source identity comes from the first valid `session_meta` in each source generation. Copied parent metadata cannot replace that identity. Where `subagent_history_start_ordinal` and record ordinals are available, earlier inherited context is omitted from the child activity projection and counted separately. This follows the native [Codex source-history boundary](https://github.com/openai/codex/blob/main/codex-rs/protocol/src/protocol.rs); sources without that evidence retain the broader inherited-history accounting limitations below.

## History and live activity

Selecting a conversation with multiple sources opens **Agent tree**, where activity labels identify the updated member. Selecting a single source opens **History**, an independent snapshot at first successful open, or retains **Live activity** if that was selected. **Live activity** shows supported records observed after startup boundaries, including the initial content of newly discovered files. The **New activity (2 min)** filter opens the tree for a multi-source conversation and the live view for an individual source. Once you choose Live activity, switching between individual sources or related agents keeps that view selected. **Metrics** summarizes indexed source records. Changing tabs does not stop directory observation.

Tool arguments/results are collapsed. The trace can be filtered by messages, tools, coordination requests, turns, and text. Supported structured coordination tool requests show their operation and target. A uniquely identified recipient can be opened directly. A request is not a delivery receipt; arbitrary shell or JavaScript strings are not interpreted as inter-agent messages. Some platforms expose additional communication formats that this version does not decode.

Each displayed message/tool includes expandable provenance: native type, source path, byte offsets, generation, timestamp, and observation phase. Reasoning/thinking payloads remain excluded. The trace follows physical record order within one source, not a reconstructed global causal order across agents.

## Debugging metrics

| Field | Meaning and limitations |
|---|---|
| Model / effort | Last recorded model and reasoning-effort configuration; not a probe of the running process. |
| Context capacity | Recorded model context-window capacity when available. Actual current context occupancy is not inferred. |
| Codex tokens | Latest native cumulative snapshot. `token_usage_record.thread_token_usage` is preferred over `event_msg:token_count.info.total_token_usage`; repeated snapshots are replaced rather than summed. |
| Claude tokens | Native message usage, replaced by message ID when the same message is observed again. Input excludes cache reads/writes; the displayed total adds these components and output when available. |
| Cache / reasoning counts | Native numeric counters when present. A missing field is unavailable, not zero. Reasoning counts do not expose reasoning text. |
| Tool calls / results | Counts of recorded request/result events in the indexed source generation. Replayed conversation records can repeat activity. |
| Flagged errors | Only explicit provider error flags; a zero count is not proof that commands or the task succeeded. |
| Paired duration | Gap between source timestamps for a matching call ID and result. Mean gap includes scheduling and logging delays; it is not model latency or pure execution time. |
| Last turn event | Recorded started/ended/interrupted event; independent from current process state or task success. |
| Accuracy / cost | Unavailable. Accuracy needs evaluation evidence; billing needs rates and native billing semantics. Neither is manufactured from a transcript. |

Metrics are per source generation, not exact cross-agent accounting. Forked/inherited/replayed records can include prior work. There is no summed multi-agent token bill, exact active-context reconstruction, automatic quality score, or semantic deduplication across sources. Indexing and record omissions are visible in status and diagnostics.

## Bounds and recovery

- Directory traversal retains at most 10,000 source paths per root, with other limits described in the [directory guide](directory-observation.md). The dashboard caps its registry at 20,000 sources.
- The live buffer retains at most 120 displayed events per source and 2,000 globally. Evicted live events remain available through source history while the provider retains them.
- Displayed content is limited to 8 KiB per event. History reads at most the first 64 MiB and returns the latest 200 supported events within that prefix. A limit notice explicitly means later history was not inspected. This is not tail seeking.
- At most two history reads run concurrently, each with a 15-second timeout. Changing selection cancels a stale request. Final symlinks and observed symlinked ancestors beneath the root are rejected; roots are trusted local directories, not an adversarial filesystem sandbox.
- Tool pairing retains up to 1,024 pending IDs and 64 named tool buckets per source. Claude message accounting retains up to 10,000 IDs per source. Overflow marks statistics partial.
- Generation changes reset that source's live timeline and metrics. Reader diagnostics indicate missing/repeated history. There are no persistent cursors: restarting establishes new boundaries.
- Missing files are retried by the directory observer. A configured root unavailable at startup is shown as unavailable; restart after creating/fixing that root. Browser network failure shows disconnected/retrying and preserves the last view.

## Local access and content

The server is read-only and loopback-only. API requests require the per-launch token from the printed URL; the browser removes it from the address bar and stores it in session storage. A new server process creates a new token. Open the new printed URL after restarting. Host/Origin checks, no CORS, and a restrictive content policy isolate the local API from unrelated websites. These are local-process protections, not multi-user authentication.

No source content is uploaded, no external assets load, and no agent files are modified. Metadata can include private paths and identifiers. `--include-content` is explicit opt-in, not automatic redaction. Closing a browser tab does not stop the observer; Ctrl-C in its terminal does. Neither action terminates, resumes, sends messages to, or otherwise controls agents.
