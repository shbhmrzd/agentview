# Copilot and Rovo Dev format evidence

Format references checked on 2026-11-13. This document records the source formats used by the experimental adapters. Source schema evidence and successful native execution are separate compatibility claims.

## Evidence levels and versions

| Provider | Installed help/version evidence | Format evidence | Initial claim boundary |
|---|---|---|---|
| GitHub Copilot CLI | `1.0.61`, including with `--no-auto-update` | Official storage documentation and pinned public SDK event types | JSONL record observation, subject to native-version validation |
| Rovo Dev | ACLI `1.3.22-stable`; cached Rovo binary `202610.27.1` | Native disposable request snapshot, bundled Pydantic AI `1.96.1` message types, matching pinned upstream source | Snapshot request ingestion supported by native evidence; response/tool projection supported by library schema, not a successful native model run |

The Copilot SDK source is pinned to [540dc4e1ccd4aa174e7602465099a2aa93fab176](https://github.com/github/copilot-sdk/blob/540dc4e1ccd4aa174e7602465099a2aa93fab176/nodejs/src/generated/session-events.ts). It is a current source snapshot, not a certification that every field exists in installed Copilot `1.0.61`.

The Atlascode source is pinned to [5a491be4ce44f0a0c02a1e3ef83221e5e61f7f50](https://github.com/atlassian/atlascode/blob/5a491be4ce44f0a0c02a1e3ef83221e5e61f7f50/src/rovo-dev/client/rovoDevApiClient.ts). Its server response models must not be relabeled as a persisted file schema.

Rovo's bundled Pydantic AI version is established by its installed `pydantic_ai_slim-1.96.1.dist-info` metadata. Static inspection of the unprotected `pydantic_ai.messages` bytecode confirms the relevant field annotations and discriminators. Matching upstream release source is pinned to [c5a0bbaa7e1d2ed19bbf580e0bee0e8720c58ea6](https://github.com/pydantic/pydantic-ai/blob/c5a0bbaa7e1d2ed19bbf580e0bee0e8720c58ea6/pydantic_ai_slim/pydantic_ai/messages.py), the commit for tag `v1.96.1`. This avoids assuming the latest online API reference describes the bundled version.

## Copilot persisted events

GitHub documents `<copilot-home>/session-state/<session-id>/events.jsonl` with adjacent workspace metadata and checkpoints. The installed environment help documents `COPILOT_HOME`, defaulting to `~/.copilot`. Read only the explicitly selected event file; workspace artifacts are not implicit permission to read more files. [Storage documentation](https://docs.github.com/en/copilot/how-tos/copilot-cli/cli-best-practices)

The event envelope contains `id`, `timestamp`, `parentId`, `type`, and `data`, with optional `agentId`, `ephemeral`, and, in the pinned current source, `checkpoint`. `id` identifies the native event; `parentId` links the previous event and is null at the beginning. `checkpoint: true` identifies a physical replay-checkpoint record. Retain both native identity and physical source occurrence. Do not silently collapse checkpoint rows or call every physical row a new semantic action. [Event-envelope documentation](https://docs.github.com/en/copilot/how-tos/copilot-sdk/features/streaming-events)

Selected payloads from the pinned generated source:

| `type` | `data` fields useful to the adapter |
|---|---|
| `session.start` | `sessionId: string`, `version: number`, `copilotVersion: string`, `producer: string`, `startTime: string`; optional `selectedModel`, `context: {cwd, gitRoot?, branch?, repository?}` |
| `user.message` | `content: string`; optional `messageId`, `source`, `interactionId`, `transformedContent`, attachments |
| `assistant.message` | `messageId: string`, `content: string`; optional `toolRequests`, `model`, `outputTokens`, `phase`, `originatingMessageId` |
| `assistant.turn_start` / `assistant.turn_end` | `turnId`; turn start may carry interaction metadata |
| `tool.execution_start` | `toolCallId: string`, `toolName: string`; optional `arguments`, `turnId`, `model`, `mcpServerName`, `mcpToolName`, deprecated `parentToolCallId` |
| `tool.execution_complete` | `toolCallId: string`, `success: boolean`; optional `result`, `error`, `turnId`, `model`, deprecated `parentToolCallId` |
| `session.error` | `errorType`, `message`, optional status information |

An assistant `toolRequests` entry has `toolCallId`, `name`, optional `arguments`, and additional provider metadata. It records a request. A matching execution-start record provides separate evidence that execution began.

Tool completion `result` is an object: `content: string`, optional `detailedContent: string`, `contents: structured-block[]`, and other fields. `content` is the model-facing representation and can be truncated; it is not a guaranteed complete terminal transcript. `error` is an object with `message: string`, optional `code`, and optional remediation information. The native `success` flag must remain distinct from AgentView process liveness and overall task correctness. Preserve absent versus false where relevant.

Privacy requires an allowlisted projection. Assistant records may also contain `reasoningText`, `reasoningOpaque`, `reasoningBlocks`, `encryptedContent`, provider payloads, and server tools. Exclude those even when user-facing content is enabled. Do not serialize arbitrary `data`, `arguments`, `contents`, error objects, or metadata into default output. Unknown event types get bounded metadata/diagnostics rather than raw payloads.

The event documentation distinguishes transient events from durable ones. In particular, `session.idle`, message deltas, reasoning deltas, usage events, and live tool progress may be ephemeral. Their absence from a file is expected, not a parsing failure. A quiet JSONL file does not prove that the native process is idle or alive. Compaction, replay checkpoints, and schema evolution also mean a file timeline should not claim to reproduce the exact active model context.

## Rovo storage and native snapshot evidence

Atlassian documents `~/.rovodev/sessions/`, `session_context.json` for history/context, and `metadata.json` for session information. It documents explicit UUID restoration and destructive history operations such as clearing/pruning. These are sufficient reasons to treat the source as a changing snapshot rather than assume an append-only event log. They do not establish atomic-write behavior, schema version, context field names, message-part serialization, or stable per-message IDs. [Session documentation](https://support.atlassian.com/rovo/docs/manage-sessions-in-rovo-dev-cli/)

Public Atlascode's `RovoDevSession` API type contains `id`, `title`, `created`, `last_saved`, `initial_prompt`, `prompts`, `latest_result`, `workspace_path`, `parent_session_id`, `num_messages`, and `log_dir`. Its [stream response types](https://github.com/atlassian/atlascode/blob/5a491be4ce44f0a0c02a1e3ef83221e5e61f7f50/src/rovo-dev/client/responseParserInterfaces.ts) use `event_kind` values such as `user-prompt`, `text`, `tool-call`, and `tool-return`. Neither establishes a disk envelope. The captured context below uses `message_history` and `part_kind`; do not replace that with the server stream format.

The cached application's own session/config modules are PyArmor-wrapped, so static inspection did not reveal their schemas. Its bundled Pydantic AI message module is unprotected. No decryption or runtime code injection was attempted.

A disposable native test created one context in its dedicated test-results persistence directory. Its top-level fields and types were inspected without emitting system prompts or other content:

```text
{
  id: string,
  message_history: array,
  deps: object,
  timestamp: integer,
  initial_prompt: string,
  prompts: array,
  latest_result: null,
  workspace_path: string,
  is_subagent: boolean,
  subagent_tool_call_id: null,
  base_log_dir: string,
  deferred_results: null,
  usage: object
}
```

The `id` matched the newly generated UUID in the directory name. The captured request was `kind: "request"`, with `parts`, string `timestamp`, string `run_id`, string `conversation_id`, and null `instructions`/`metadata`. Parts had `part_kind: "system-prompt"` or `"user-prompt"`, string `content` and ISO timestamp; system parts also had nullable `dynamic_ref`. The context's numeric timestamp has not been certified as a particular epoch unit; prefer the explicit message/part timestamp strings.

The native `metadata.json` **did not contain a session ID**. It held `workspace_path`, nullable `title`, `fork_data`, `context_pressure`, `compaction_threshold_tokens`, plus `bookmarks` and `is_manual_title`. This differs materially from the Atlascode API summary type. Do not require an `id` in metadata or copy the API summary schema into the disk decoder. Arbitrary `deps`, embedded artifacts, usage metadata, system prompts, and instructions remain outside user-facing content projection.

### Response and tool parts: library evidence, not captured native output

The probe failed before recording a model response. The following fields are verified against the bundled library and matching release source; original synthetic response/tool fixtures may use them, but must not be labeled successful Rovo captures:

| Message/part | Supported shape and interpretation |
|---|---|
| `ModelResponse` | `kind: "response"`, `parts`, ISO `timestamp`, optional/null `model_name`, `run_id`, `conversation_id`, provider fields, usage, and `state` (library default `"complete"`) |
| `TextPart` | `part_kind: "text"`, `content: string`; optional/null `id`, `provider_name`, `provider_details` |
| `ToolCallPart` | `part_kind: "tool-call"`, `tool_name: string`, `args: string | object | null`, `tool_call_id: string`; optional/null part/provider metadata. Appears in a response; records a request, not confirmed execution |
| `ToolReturnPart` | `part_kind: "tool-return"`, `tool_name`, `content`, `tool_call_id`, ISO `timestamp`, optional metadata, `outcome: "success" | "failed" | "denied"` (library default `"success"`). Appears in a request message |
| `UserPromptPart` | `part_kind: "user-prompt"`; library permits text or a sequence containing multimodal items. The native probe contained text only |
| `RetryPromptPart` | `part_kind: "retry-prompt"`, text or validation-error content, optional tool name, tool-call ID and timestamp. This is retry feedback, not a new human message |
| `ThinkingPart` | `part_kind: "thinking"` with content/signature/provider fields. Exclude its payload even when normal content is enabled |

Tool return content is not limited to a string: the library supports structured and multimodal values. A narrow initial projection may accept text only and report unsupported representations; it must not serialize all nested content or provider metadata by default. Preserve recorded `outcome` rather than inventing a boolean from the text. If the field is absent, an observer should retain uncertainty instead of pretending the library's construction default was explicitly recorded. Optional text-part IDs are not guaranteed message IDs, and `run_id`/`conversation_id` must not be substituted for the snapshot's native session `id`.

A snapshot adapter should bound total bytes and messages, read a complete candidate, validate structure before publishing, and preserve the last good observation on incomplete/malformed rewrites. Emit an explicit snapshot revision and source provenance; unchanged snapshots may be suppressed. Replacements/removals are new observations of current history, not proof of incremental execution. If no native session ID exists in the context, require an explicit identity hint or a separately selected metadata source. A content hash is an observation ID, not a native session ID. Metadata and context may be saved separately; never assume a transaction across them.

## Native validation limits

Copilot native execution was blocked by account policy; its adapter is validated with original synthetic fixtures and process tests against public schema evidence.

The disposable Rovo probe persisted request history but failed before recording an assistant response. Request ingestion has native evidence; response and tool decoding are supported by the matching library schema and synthetic fixtures. This is not a successful native response or live-follow test.
