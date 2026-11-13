# Synthetic attachment fixtures

These are original, hand-authored records with fictional session identities, paths, messages, and commands. They are not captured user sessions and are not proof of compatibility with a particular installed binary. They are never executed as commands.

Shapes were checked against the following pinned primary sources on 12 November 2026:

- [Claude SDK transcript tests](https://github.com/anthropics/claude-agent-sdk-python/blob/3cf3446b0fa6c97042e85b245bba07459f22f946/tests/test_sessions.py) and [transcript parser](https://github.com/anthropics/claude-agent-sdk-python/blob/3cf3446b0fa6c97042e85b245bba07459f22f946/src/claude_agent_sdk/_internal/sessions.py).
- [Codex rollout recorder tests](https://github.com/openai/codex/blob/d2f76324729d7c4a2b3225cfe42c7dbf162d98b7/codex-rs/rollout/src/recorder_tests.rs) and [protocol definitions](https://github.com/openai/codex/blob/d2f76324729d7c4a2b3225cfe42c7dbf162d98b7/codex-rs/protocol/src/protocol.rs).

Additional shapes checked on 13 November 2026:

- Copilot: [SDK session event types](https://github.com/github/copilot-sdk/blob/540dc4e1ccd4aa174e7602465099a2aa93fab176/nodejs/src/generated/session-events.ts). The fixture distinguishes tool request from execution-start and retains native envelope IDs separately from message IDs.
- Rovo: a deliberately created native request-only snapshot from cached Rovo Dev 202610.27.1 established the `id`/`message_history` wrapper and request parts. Response/tool shapes were verified against the bundled Pydantic AI 1.96.1 annotations and [pinned public types](https://github.com/pydantic/pydantic-ai/blob/c5a0bbaa7e1d2ed19bbf580e0bee0e8720c58ea6/pydantic_ai_slim/pydantic_ai/messages.py). All fixture messages, IDs, timestamps, and tool content were hand-authored. The response/tool fixture is not a successful native capture.

Full evidence and blocked native outcomes are in [provider-format-evidence.md](../docs/provider-format-evidence.md). No real transcripts, provider configurations, or account data are committed as fixtures.

The reader presents physical record order. Claude parent chains may branch or compact; this experiment does not reconstruct the conversation graph. Codex's `session_meta.payload.id` is the thread identifier, while optional `session_id` is retained separately. Not every current Codex storage format is a plain JSONL rollout. Tool request records do not establish execution. Tool output does not necessarily carry a structured exit code.
