# Debugging coding-agent sessions

AgentView helps answer questions about recorded work: what a tool was asked to do, what it returned, which child source is linked to a delegation, and what was said afterward. It reads supported local session files without installing an SDK in your application or taking over the agent process.

Start with the [README quickstart](../README.md#open-the-dashboard). Use `--include-content` to inspect prompts and payloads; those fields are hidden by default. The browser supports Claude Code and Codex. A conversation may contain several sources, so select the relevant agent before searching its history. Search session scans that selected source, not every agent in the family.

## Try the demo

From the repository root, after `make build`:

```sh
./bin/agentview serve --agent codex \
  --dir "$PWD/testdata/debugging-demo" \
  --include-content --listen 127.0.0.1:0
```

Open the complete URL printed in the terminal. This serves only the bundled synthetic files on an available local port. No agent account is needed, no commands in the transcript run, and no model is called. The example's timestamps are fixed; choose **All history** in the conversation list rather than New activity.

1. Select **Investigate the failing discount test…**. Its **Agent tree** contains the main agent and `pricing_review`.
2. On the **Main agent** card, choose **Open trace →** to open its history. Select **Failures** and click **Search session**.
3. Expand the `exec_command` result. It records exit code `1` and `got 900 cents, want 800 cents`.
4. Expand **Matched input payload** to see `go test ./...`. **Open full request** opens the matching request in a fresh history search; **Find result** takes you back to its result. Expand that result again and use **Find following reply** to read the parent's next recorded response.
5. Open **Agent tree**, expand `pricing_review`, then **Inspect messages & provenance → Recorded spawn request & result**. The matched input shows the review instructions; the result contains a child reference. **Open linked child →** opens the reviewer's source.
6. Read the reviewer's finding: the test supplies a 10% discount while expecting 20%. Return to the main agent to inspect its recorded response. There is no successful rerun in this example, so the trace does not establish a fix.

The demo is historical. To try live observation, start AgentView against your normal agent directories and continue a session in the agent's own application. Stop the demo with Ctrl-C when finished.

## Find why a tool or test failed

**Question:** “The agent said it finished, but a test or command may have failed.”

1. Find the conversation by project, request excerpt or session ID. Select the main agent or child that ran the command, then **History**.
2. Choose **Failures** and click **Search session**. The event filter alone applies to the displayed page; Search session scans through the source's initial file boundary. Use **Next page** when more matches are available.
3. Expand a result and read its outcome basis: an explicit provider error flag or a supported process-exit envelope. Inspect the result text and **Matched input payload**. Use **Open full request** when the carried input excerpt is clipped.
4. Use **Find following reply** to inspect the next recorded assistant message after that result. A sentence saying “done” is separate evidence from the command's exit status.
5. Expand **Record provenance** for the native record type, call ID, source path and byte range. **Clear session search** returns to chronological history.

This can reveal a wrong argument, working directory, failing assertion, or failure that the later reply did not explain. An empty Failures search does not prove success: unsupported tool formats, absent records and unknown outcomes can hide evidence. Check **Metrics**, **History coverage** and **View diagnostics** when results look incomplete.

## Understand a delegated task

**Question:** “Why did the main agent spawn this child, and what did it return?”

1. Open the conversation's **Agent tree** and expand the child.
2. Open **Inspect messages & provenance → Recorded spawn request & result**, when available. The matched input contains the recorded delegation instructions. A spawn result may contain only a child ID or path; it is not the child's answer.
3. Choose **Open linked child →** or **Open trace →** and inspect the child's own messages and tool results. Use **History** for earlier records or **Live activity** for newly observed work.
4. Return to the main agent. Use **Coordination → Search session** to locate recorded communication requests and messages, or **Messages → Search session** with a distinctive phrase to find what the parent said afterward.

The tree's first-request and latest-reply excerpts are shortcuts. A first request may include copied context, and a latest reply may be progress rather than a final answer. Missing or ambiguous spawn links remain gaps. AgentView does not establish that a message was delivered or that the parent used a child's result.

## Find the session that is changing now

**Question:** “I have several agent windows open. Which conversation is receiving updates?”

1. Leave `agentview serve --include-content` running against your normal directories.
2. Continue the session in Claude Code or Codex. In AgentView choose **New activity (2 min)** or click the activity count.
3. Open the conversation. For a family, inspect the activity label on each tree node to find the member with new records. Select that source and choose **Live activity**.

**New activity** means appended conversation events were observed. **Recent file activity** can mean only that a file changed or was discovered. **No recent updates** cannot distinguish a stopped process from an agent waiting or working silently. Keep using the native agent application for approval prompts, interruptions and process state.

## Investigate repeated work or context changes

**Question:** “Why did the agent repeat a command or lose track of earlier work?”

1. In **History**, choose **Tools**, enter a distinctive command or argument in the trace search field, then click **Search session**. Compare requests, results and source positions across the matches.
2. Use **Open full request** and **Find following reply** around suspicious calls. If an ID is reused, the correlation warning matters; do not assume the nearest displayed result belongs to the request.
3. Clear the search, choose **Context changes**, and click **Search session** to locate supported compaction markers. Return to chronological history to inspect nearby messages.
4. Check **Metrics** for recorded usage and tool counts as supporting context. Counts do not establish how much work was necessary, exact billing, or the active contents of the model's context.

This helps collect evidence for a repetition or context-loss hypothesis. A compaction marker alone does not prove why the agent changed behavior, and AgentView cannot reconstruct every discarded or rewritten record.

## Recover an incomplete investigation

| What you see | What to do |
|---|---|
| Old events missing from Live activity | Open History. Live activity keeps a bounded recent buffer. |
| No search match on the current page | Click Search session to scan the selected source. Check whether the work belongs to a child source. |
| No new append in History | Use Live activity, or Refresh history for a new initial boundary. Existing history reads exclude later appends. |
| Expired cursor or a source that changed/became unreadable | Restore the source if necessary, then use Refresh history. The UI preserves the displayed page; the old cursor may no longer be usable. |
| Transient fetch failure with Next page offered | Retry Next page. Follow the displayed recovery action. |
| Unknown result or missing request/child link | Inspect provenance and diagnostics. A missing record, unsupported format, reused ID or evidence limit may prevent a match. |
| Oversized record diagnostic | The default record limit is 1 MiB. If needed, restart with a larger `--max-record-bytes` (up to 16 MiB) and read history again. Larger limits require more memory; the source file is unchanged. |

## Share evidence with a maintainer

**Export redacted evidence** downloads structural JSONL for the selected source: provider, timestamps, outcome evidence, usage and aliased references. It omits prompt text, payloads, titles, paths and raw diagnostics. Check the final coverage record; a false or missing completion footer means the export is incomplete.

Review even a structural export before sharing: timestamps and usage can still be sensitive. Include the AgentView/provider versions, operating system, reproduction steps and what you expected to see. For a content-parsing bug, write a minimal synthetic example rather than uploading the real transcript. See [contribution guidance](../CONTRIBUTING.md#bug-reports-and-fixtures).

The export currently covers one source and has no import/viewer workflow. It is supporting evidence for a report, not a portable replay of the full agent family.
