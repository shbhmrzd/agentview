# Debugging walkthrough data

These two hand-authored Codex-shaped JSONL files describe a fictional discount-test investigation. All messages, identifiers, timestamps, file paths and command outputs are synthetic. The referenced checkout and test code are not part of this repository.

Opening the files in AgentView only reads them. It does not run the displayed commands, create agents, contact a model, or access a native agent store.

The main source records a failing test, a follow-up reply, a spawn request/result and a reviewer message. The child source records the review task, an inspection result and its findings. The example deliberately has no successful rerun: finding a possible cause does not establish that a bug was fixed.

See the [debugging guide](../../docs/debugging.md#try-the-demo) for the walkthrough.
