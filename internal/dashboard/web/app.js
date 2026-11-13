import {visibleSessions, prettyContent, age, relatedSessions, resolveTarget, filterEvents, sessionActivity, agentForest} from './helpers.mjs';
import {createTour} from './tour.mjs';
import {groupConversations, visibleConversations, conversationActivity, shortID} from './conversations.mjs';

const $ = id => document.getElementById(id);
const fragment = new URLSearchParams(location.hash.slice(1));
let token = fragment.get('token') || '';
try { token ||= sessionStorage.getItem('agentview-token') || ''; if (fragment.has('token')) sessionStorage.setItem('agentview-token', token); } catch { /* The current URL token still works when storage is disabled. */ }
if (fragment.has('token')) history.replaceState(null, '', location.pathname);
let snapshot = {sessions: [], roots: [], diagnostics: {}}, provider = 'all', selected = '', mode = 'history';
let historyController, historyVersion = 0, currentHistory = null, currentLive = [], lastTimeline = '', lastRows = '', connected = false;
let detailedSession = null, currentTree = null;
let inventory = groupConversations([]), listMode = 'conversations';
let tourStorage, everConnected = false;
try { tourStorage = localStorage; } catch { /* Private browser settings may disable storage. */ }
const tour = createTour({document, storage: tourStorage, getContext: () => ({sessions: snapshot.sessions.length, includeContent: snapshot.include_content})});

function element(tag, className, text) { const el = document.createElement(tag); if (className) el.className = className; if (text != null) el.textContent = text; return el; }
async function api(path, signal) {
  const response = await fetch(path, {headers: {Authorization: `Bearer ${token}`}, cache: 'no-store', signal});
  if (!response.ok) throw new Error(await response.text());
  return response.json();
}
function notice(text) { $('notice').textContent = text; $('notice').hidden = !text; }
function session() { return detailedSession?.id === selected ? detailedSession : snapshot.sessions.find(s => s.id === selected); }
function number(n) { return n == null ? 'Unavailable' : new Intl.NumberFormat().format(n); }
function agentName(s) { return s.context?.agent_path?.split('/').filter(Boolean).pop() || s.context?.name || s.context?.agent_id || (s.context?.parent_thread_id ? (s.session_id ? `${s.session_id.slice(0, 8)}…${s.session_id.slice(-4)}` : 'Child agent') : 'Main agent'); }
function navigateAgent(id) { provider = 'all'; $('window').value = 'all'; $('search').value = ''; selectSession(id); }

function renderFamily() {
  const s = session(); if (!s) return;
  const family = relatedSessions(snapshot.sessions, selected);
  const pane = $('agent-family'), signature = JSON.stringify([family.map(a => [a.id, a.context, a.status, sessionActivity(a, connected).kind]), selected]);
  if (pane.dataset.version !== signature) {
    pane.dataset.version = signature; pane.replaceChildren();
    pane.append(element('span', 'family-label', `AGENTS · ${family.length}`));
    for (const agent of family.slice(0, 100)) {
      const button = element('button', `agent-node ${agent.id === selected ? 'active' : ''}`, `${agent.context?.parent_thread_id ? '↳ ' : ''}${agentName(agent)}`);
      const activity = sessionActivity(agent, connected);
      button.classList.toggle('has-updates', activity.kind === 'new');
      button.title = `${agent.session_id || agent.id} · ${activity.label}${agent.context?.parent_thread_id ? ` · parent ${agent.context.parent_thread_id} (${agent.parent_evidence})` : ''}`;
      button.setAttribute('aria-label', `${button.textContent}${activity.kind === 'new' ? ' · New activity' : ''}`);
      button.addEventListener('click', () => navigateAgent(agent.id)); pane.append(button);
    }
    if (family.length > 100) pane.append(element('span', 'muted', 'Showing first 100 agents; all remain searchable.'));
    if (s.context?.parent_thread_id && !family.some(a => a.session_id === s.context.parent_thread_id)) pane.append(element('span', 'muted', `Parent ${s.context.parent_thread_id.slice(0, 12)} is not in observed sources.`));
  }
  const m = s.metrics, strip = $('metric-strip'); strip.replaceChildren();
  for (const [label, value] of [['Reported tokens', number(m.usage?.total_tokens)], ['Tool calls', number(m.tool_calls)], ['Flagged errors', number(m.reported_errors)], ['Model', s.context?.model || 'Not recorded']]) {
    const metric = element('div', 'metric'); metric.append(element('span', '', label), element('strong', '', value)); strip.append(metric);
  }
}

function metricRow(label, value) { const row = element('div', 'metric-row'); row.append(element('span', '', label), element('strong', '', value)); return row; }
function renderMetrics(pane) {
  const s = session(), m = s.metrics, u = m.usage || {};
  $('view-note').textContent = `Metrics from indexed records in source generation ${s.generation}. ${s.status === 'indexing' ? 'Indexing is still in progress. ' : ''}${m.partial ? 'Some records/correlations were omitted; counts are partial. ' : ''}These are source observations, not billing or correctness scores.`;
  pane.append(element('h3', '', 'Model & token usage'));
  for (const [label, value] of [['Model', s.context?.model || 'Not recorded'], ['Reasoning effort', s.context?.effort || 'Not recorded'], ['Context capacity', number(s.context?.context_window)], ['Input tokens (provider semantics)', number(u.input_tokens)], ['Cache read tokens', number(u.cached_input_tokens)], ['Cache write tokens', number(u.cache_write_input_tokens)], ['Output tokens', number(u.output_tokens)], ['Reasoning output tokens', number(u.reasoning_output_tokens)], ['Total tokens', number(u.total_tokens)], ['Usage source', m.usage_basis || 'Not recorded'], ['Usage recorded at', m.usage_at ? new Date(m.usage_at).toLocaleString() : 'Not recorded']]) pane.append(metricRow(label,value));
  pane.append(element('p', 'muted', 'Codex input includes cached reads. Claude input excludes cache reads and writes. Cumulative counters are replaced, never summed; inherited fork history can be included. No cross-agent billing total is inferred.'));
  pane.append(element('h3', '', 'Tool activity'));
  for (const [name, tool] of Object.entries(m.tools || {}).sort((a,b) => b[1].calls-a[1].calls)) {
    pane.append(metricRow(name, `${tool.calls} calls · ${tool.results} paired results · ${tool.errors} flagged errors${tool.timed ? ` · ${Math.round(tool.total_ms/tool.timed)} ms mean recorded gap (${tool.timed} pairs)` : ''}`));
  }
  if (!Object.keys(m.tools || {}).length) pane.append(element('p', 'muted', 'No tool calls indexed.'));
  pane.append(element('p', 'muted', 'Time is the gap between recorded call and result timestamps, not model latency or pure tool execution time. Errors count only explicit provider error flags; zero does not prove success.'));
  pane.append(element('h3', '', 'Outcome & relationships'));
  pane.append(metricRow('Last recorded turn event', m.last_turn || 'Not recorded'), metricRow('Message blocks', number(m.messages)), metricRow('Turns started', number(m.turns)), metricRow('Accuracy / task success', 'Unavailable — requires evaluation evidence'), metricRow('Cost', 'Unavailable — no billing data'), metricRow('Process state', 'Unknown'), metricRow('Parent thread', s.context?.parent_thread_id || 'Not recorded'), metricRow('Relationship evidence', s.parent_evidence || 'Not recorded'));
  pane.append(metricRow('Inherited context records omitted', number(m.inherited_records)));
  for (const [label, value] of [['Agent path', s.context?.agent_path], ['Agent nickname', s.context?.name], ['Agent role', s.context?.role], ['Native agent ID', s.context?.agent_id]]) if (value) pane.append(metricRow(label,value));
  if (s.context?.forked_from_id) {
    const target = resolveTarget(snapshot.sessions, s, s.context.forked_from_id);
    const row = metricRow('Forked from', s.context.forked_from_id);
    if (target) { const button = element('button', 'agent-node', 'Open fork source'); button.onclick = () => navigateAgent(target.id); row.append(button); } pane.append(row);
  }
}

function provenance(e) {
  const details = element('details', 'provenance'); details.append(element('summary', '', 'Record provenance'));
  details.append(element('pre', '', `${e.native_type || e.kind}\n${e.source?.path || ''}\nGeneration ${e.source?.generation ?? '?'} · bytes ${e.source?.offset ?? '?'}–${e.source?.end_offset ?? '?'}\n${e.time || 'No source timestamp'} · ${e.phase || 'phase unavailable'}${e.call_id ? `\nCall ID: ${e.call_id}` : ''}`));
  return details;
}

function renderSources() {
  const units = listMode === 'conversations' ? inventory.conversations : snapshot.sessions;
  const sourceData = JSON.stringify([snapshot.roots, units.map(s => s.agent), provider, listMode]);
  if ($('sources').dataset.version === sourceData) return;
  $('sources').dataset.version = sourceData;
  $('sources').replaceChildren();
  for (const root of snapshot.roots) {
    const button = element('button', `nav ${provider === root.agent ? 'selected' : ''}`);
    button.dataset.provider = root.agent;
    button.title = `${units.filter(s => s.agent === root.agent).length} ${listMode} · ${root.path}`;
    button.append(element('span', `provider-icon ${root.agent}`, root.agent === 'codex' ? '›_' : '✳'), document.createTextNode(root.agent === 'codex' ? 'Codex' : 'Claude Code'), element('b', '', units.filter(s => s.agent === root.agent).length));
    $('sources').append(button, element('div', `root-state ${root.status}`, root.status === 'watching' ? 'Watching local files' : root.status));
  }
  document.querySelector('[data-provider="all"]').classList.toggle('selected', provider === 'all');
}

function renderList() {
  const grouped = listMode === 'conversations';
  const rows = grouped ? visibleConversations(inventory.conversations, provider, $('search').value, $('window').value) : visibleSessions(snapshot.sessions, provider, $('search').value, $('window').value);
  const ownsSelection = row => grouped ? row.members.some(s => s.id === selected) : row.id === selected;
  const activityFor = row => grouped ? conversationActivity(row,connected) : sessionActivity(row,connected);
  $('total-count').textContent = grouped ? inventory.conversations.length : snapshot.sessions.length;
  $('all-label').textContent = grouped ? 'All conversations' : 'All sources';
  $('page-title').textContent = grouped ? 'Conversations' : 'All sources';
  $('page-description').textContent = grouped ? 'Main conversations, with child agents grouped inside.' : 'Individual transcript files, including child agents and duplicate exports.';
  $('recent-count').textContent = connected ? (grouped ? visibleConversations(inventory.conversations,'all','','updates').length : visibleSessions(snapshot.sessions,'all','','updates').length) : '—';
  $('recent-label').textContent = `${grouped ? 'conversations' : 'sources'} with new activity · 2 min`;
  $('visible-count').textContent = `${rows.length} ${grouped ? (rows.length === 1 ? 'conversation' : 'conversations') : (rows.length === 1 ? 'source' : 'sources')}`;
  $('inventory-note').textContent = grouped ? `${inventory.conversations.length} conversations from ${snapshot.sessions.length} sources · children grouped by recorded IDs` : `${snapshot.sessions.length} transcript files · source rows can belong to the same conversation`;
  $('list-foot').textContent = grouped ? 'Sorted by activity across the whole conversation' : 'Sorted by source activity · updated every second';
  renderSources();
  const signature = JSON.stringify([rows, selected, listMode, connected, rows.map(row => activityFor(row).kind), Math.floor(Date.now()/60000)]);
  if (signature === lastRows) return;
  lastRows = signature;
  const focusedID = document.activeElement?.dataset.row;
  const scroll = $('session-list').scrollTop;
  $('session-list').replaceChildren();
  if (!rows.length) $('session-list').append(element('div', 'empty-list', $('window').value === 'updates' ? 'No new transcript activity observed in the last 2 minutes. Send a message in Claude or Codex, or choose Last 24 hours.' : snapshot.sessions.length ? 'No matches. Try All history or clear your search.' : 'Discovering session files… Check source diagnostics if this stays empty.'));
  for (const s of rows) {
    const source = grouped ? s.primary : s;
    const row = element('button', `session-row ${ownsSelection(s) ? 'active' : ''}`);
    row.dataset.row = s.id; row.dataset.session = source.id;
    if (grouped) row.dataset.conversation = s.id;
    row.setAttribute('aria-pressed', String(ownsSelection(s)));
    const top = element('div', 'row-top');
    const title = element('span', 'row-title', grouped ? s.title : source.title || `${source.context?.parent_thread_id ? 'Child source' : 'Source'} ${shortID(source.session_id || source.id)}`);
    title.title = (grouped ? s.title_from_request : !!source.title) ? 'Excerpt from the first recorded user request' : 'No request title available';
    top.append(element('span', `provider-icon ${s.agent}`, s.agent === 'codex' ? '›_' : '✳'), title, element('time', 'row-time', age(s.last_activity)));
    const bottom = element('div', 'row-bottom');
    const activity = activityFor(s);
    const activityBadge = element('span', `activity-label activity-${activity.kind}`, activity.label); activityBadge.title = activity.detail;
    bottom.append(activityBadge);
    if (grouped) {
      bottom.append(element('span', '', `${s.source_count} ${s.source_count === 1 ? 'source' : 'sources'}${s.child_count ? ` · ${s.child_count} child sources` : ''}`));
      if (s.main_missing) bottom.append(element('span', 'row-problem', 'Main source unavailable'));
      if (s.members.some(member => member.id === selected) && selected !== s.primary.id) bottom.append(element('span', '', 'Viewing an agent in this conversation'));
    } else if (source.context?.parent_thread_id) bottom.append(element('span', '', `Child of ${shortID(source.context.parent_thread_id)}`));
    else if (source.live_count) bottom.append(element('span', '', `${source.live_count} new events`));
    row.append(top, element('div', 'row-sub', `${s.project || 'Project unknown'} · ${shortID(grouped ? s.conversation_id : source.session_id || source.id)}`), bottom);
    $('session-list').append(row);
    if (focusedID === s.id) row.focus({preventScroll: true});
  }
  $('session-list').scrollTop = scroll;
  // A filter must not leave an unrelated, hidden selection in the detail pane.
  if (!rows.some(ownsSelection)) {
    if (rows.length) { if (grouped) selectConversation(rows[0]); else selectSession(rows[0].id); }
    else { selected = ''; historyController?.abort(); historyVersion++; $('empty-detail').hidden = false; $('selected-detail').hidden = true; }
  }
}

function selectConversation(conversation) {
  // Multi-source families open the overview, so an active child's updates
  // are not hidden behind a quiet main transcript.
  selectSession(conversation.primary.id, conversation.source_count > 1 || conversation.main_missing ? 'agents' : undefined);
}

function renderHeading() {
  const s = session(); if (!s) return;
  $('empty-detail').hidden = true; $('selected-detail').hidden = false;
  $('selected-detail').classList.toggle('tree-view', mode === 'agents');
  $('detail-provider').textContent = s.agent === 'codex' ? 'CODEX' : 'CLAUDE CODE';
  $('detail-project').textContent = s.project;
  $('detail-id').textContent = s.session_id || 'Session identity pending';
  const activity = sessionActivity(s, connected);
  $('detail-status').textContent = activity.label;
  $('detail-status').className = `badge activity-${activity.kind}`;
  $('detail-status').title = activity.detail;
  $('activity-hint').textContent = activity.kind === 'new' ? `New transcript activity observed ${age(s.last_update_at)}. ${mode === 'live' ? 'Following new events.' : 'Open Live activity to follow along.'} Process state remains unknown.` : activity.detail;
  $('detail-directory').textContent = s.directory || 'Not recorded by provider';
  $('detail-path').textContent = s.path;
  $('live-count').textContent = s.live_count;
  $('history-tab').classList.toggle('active', mode === 'history');
  $('live-tab').classList.toggle('active', mode === 'live');
  $('refresh-history').hidden = mode !== 'history';
  $('metrics-tab').classList.toggle('active', mode === 'metrics');
  $('agents-tab').classList.toggle('active', mode === 'agents');
  $('trace-filters').hidden = mode === 'metrics' || mode === 'agents';
  renderFamily();
}

function renderTimeline(force = false) {
  renderHeading();
  let entries = filterEvents(mode === 'history' ? currentHistory?.events || [] : currentLive, $('event-type').value, $('trace-search').value);
  const signature = JSON.stringify([selected, mode, entries, currentHistory?.read_at, currentHistory?.error, mode === 'metrics' ? session()?.metrics : null, mode === 'agents' ? [currentTree, connected, currentTree?.sessions?.map(s => sessionActivity(s, connected).kind)] : null]);
  if (!force && signature === lastTimeline) return;
  lastTimeline = signature;
  const pane = $('timeline');
  const atBottom = pane.scrollHeight - pane.scrollTop - pane.clientHeight < 70;
  const scroll = pane.scrollTop;
  const openTools = new Set([...pane.querySelectorAll('details[open]')].map(x => x.dataset.key));
  const treeExpansion = new Map([...pane.querySelectorAll('.tree-node, .tree-inspect, .tree-message')].map(x => [x.dataset.key, x.open]));
  const focused = document.activeElement;
  const focusedTreeKey = pane.contains(focused) ? focused.closest('details')?.dataset.key : null;
  const focusedTreeAction = focused.classList?.contains('tree-open') ? '.tree-open' : ':scope > summary';
  pane.replaceChildren();
  if (mode === 'agents') {
    renderAgentTree(pane, treeExpansion); pane.scrollTop = force ? 0 : scroll;
    if (focusedTreeKey) [...pane.querySelectorAll('details[data-key]')].find(node => node.dataset.key === focusedTreeKey)?.querySelector(focusedTreeAction)?.focus({preventScroll:true});
    return;
  }
  if (mode === 'metrics') { renderMetrics(pane); pane.scrollTop = force ? 0 : scroll; return; }
  if (mode === 'history') {
    $('view-note').textContent = !currentHistory ? 'Reading a bounded history snapshot… Live observation continues.' : currentHistory.error || `History snapshot · ${new Date(currentHistory.read_at).toLocaleTimeString()} · ${entries.length} events${currentHistory.dropped ? ` (latest 200; ${currentHistory.dropped} earlier events omitted)` : ''}${currentHistory.diagnostics ? ` · ${currentHistory.diagnostics} diagnostic records omitted` : ''}. Live activity is in the next tab.${currentHistory.limited ? ' Read limit reached: only the first 64 MiB were inspected; later history is missing.' : ''}`;
  } else {
    $('view-note').textContent = `Activity observed since ${new Date(snapshot.started_at).toLocaleTimeString()} · latest ${entries.length} retained events. New files include their existing records; source replacements start a fresh timeline.`;
  }
  if (!entries.length) pane.append(element('p', 'muted', $('event-type').value !== 'all' || $('trace-search').value ? 'No events match these trace filters.' : mode === 'history' ? (!currentHistory ? 'Loading conversation…' : currentHistory.error ? 'Use Refresh history to retry.' : 'No supported conversation events in this snapshot.') : 'Waiting for new activity. Send a message in your agent to see updates here. Earlier messages are available under History.'));
  const toolNames = new Map();
  for (const [index, e] of entries.entries()) {
    if (e.tool && e.call_id) toolNames.set(e.call_id, e.tool);
    if (e.kind.startsWith('turn.')) { pane.append(element('div', 'turn', `${e.kind.replace('turn.', 'Turn ')} · ${new Date(e.time).toLocaleTimeString()}`)); continue; }
    if (e.kind.startsWith('tool.')) {
      const details = element('details', `tool ${e.kind === 'tool.requested' ? 'tool-request' : 'tool-result'} ${e.error ? 'error' : ''}`);
      details.dataset.key = `${selected}:${mode}:${e.sequence || index}`;
      details.open = openTools.has(details.dataset.key);
      details.append(element('summary', '', `${e.kind === 'tool.requested' ? '↗ Tool call' : '↙ Tool result'} · ${e.tool || toolNames.get(e.call_id) || e.call_id || 'tool'}${e.error ? ' · error' : ''}${e.duration_ms != null ? ` · ${e.duration_ms} ms recorded gap` : ''}`), element('div', 'payload-label', e.kind === 'tool.requested' ? 'Input payload' : 'Recorded result'), element('pre', '', e.captured ? prettyContent(e.content) : 'Payload hidden. Restart with --include-content to inspect it.'));
      if (e.coordination) {
        const target = resolveTarget(snapshot.sessions, session(), e.coordination.target);
        const row = element('div', 'coordination', `Coordination request · ${e.coordination.operation}${e.coordination.target ? ` → ${e.coordination.target}` : ''}. Delivery is not established by this call.`);
        if (target) { const button = element('button', 'agent-node', 'Open recipient'); button.onclick = () => navigateAgent(target.id); row.append(button); }
        else if (e.coordination.target) row.append(element('span', 'muted', ' Recipient not uniquely identified in observed sources.'));
        details.append(row);
      }
      details.append(provenance(e));
      if (e.clipped) details.append(element('p', 'clipped', 'Display limited to 8 KiB.'));
      pane.append(details); continue;
    }
    const isUser = e.kind === 'message.user';
    const article = element('article', `event ${isUser ? 'user' : 'assistant'}`), meta = element('div', 'event-meta');
    meta.append(element('span', 'event-icon', isUser ? 'U' : 'A'), element('strong', '', isUser ? 'You' : session()?.agent === 'claude' ? 'Claude' : 'Codex'), element('time', '', new Date(e.time).toLocaleTimeString()));
    article.append(meta, element('div', 'event-content', e.captured ? e.content : 'Message content hidden. Restart with --include-content to read messages.'));
    article.append(provenance(e));
    if (e.clipped) article.append(element('p', 'clipped', 'Display limited to 8 KiB.'));
    pane.append(article);
  }
  if (force || (mode === 'live' && atBottom)) pane.scrollTop = pane.scrollHeight; else pane.scrollTop = scroll;
}

async function loadHistory() {
  historyController?.abort(); historyController = new AbortController();
  const version = ++historyVersion, id = selected;
  currentHistory = null; renderTimeline(true);
  try {
    const value = await api(`/api/history?id=${encodeURIComponent(id)}`, historyController.signal);
    if (version !== historyVersion || id !== selected) return;
    currentHistory = value; if (mode === 'history') renderTimeline(true);
  } catch (error) {
    if (error.name === 'AbortError' || version !== historyVersion) return;
    currentHistory = {events: [], error: error.message}; if (mode === 'history') renderTimeline(true);
  }
}
function selectSession(id, view) {
  selected = id; mode = view || (mode === 'live' || $('window').value === 'updates' ? 'live' : 'history'); currentLive = []; lastTimeline = ''; currentHistory = null; detailedSession = null; currentTree = null;
  historyController?.abort(); historyVersion++;
  $('event-type').value = 'all'; $('trace-search').value = '';
  renderList(); renderTimeline(true);
  if (mode === 'history') loadHistory(); else if (mode === 'agents') loadAgentTree(); else loadLive().catch(error => notice(error.message));
}

function renderAgentTree(pane, expansion) {
  $('view-note').textContent = 'Recorded parent → child links. Requests and replies are excerpts; a latest reply is not necessarily a final result. Group membership alone does not establish a parent.';
  if (!currentTree || currentTree.error) { pane.append(element('p', 'muted', currentTree?.error || 'Loading agent relationships and message excerpts…')); return; }
  if (currentTree.limited) pane.append(element('p', 'notice', 'Showing at most 100 related sources. Some parents or children may be outside this view.'));
  if (!snapshot.include_content) pane.append(element('p', 'muted', 'Start with --include-content to see recorded requests and replies.'));
  const list = element('ul', 'agent-tree'); list.setAttribute('aria-label', 'Agent relationships');
  const preview = (entry, fallback) => entry?.content ? prettyContent(entry.content) : fallback;
  function branch(node, depth) {
    const s = node.session, overview = s.overview || {}, item = element('li', 'tree-branch');
    const details = element('details', `tree-node ${s.id === selected ? 'selected' : ''}`);
    details.dataset.key = `agent:${s.id}`;
    details.open = expansion.has(details.dataset.key) ? expansion.get(details.dataset.key) : depth === 0 || s.id === selected;
    const heading = element('summary', ''), top = element('div', 'tree-heading');
    const activity = sessionActivity(s, connected);
    top.append(element('strong', '', agentName(s)), element('span', `activity-label activity-${activity.kind}`, activity.label));
    heading.append(top, element('span', 'tree-identity', `${s.session_id || 'Identity pending'}${node.children.length ? ` · ${node.children.length} direct child sources` : ''}`));
    for (const [label, entry, empty] of [['First request', overview.first_request, 'Not available in indexed records'], ['Latest reply', overview.latest_reply, 'No recorded reply available']]) {
      const line = element('div', 'tree-excerpt'); line.append(element('b', '', `${label}: `), document.createTextNode(preview(entry, snapshot.include_content ? empty : 'Content hidden'))); heading.append(line);
    }
    details.append(heading);
    const body = element('div', 'tree-body');
    if (node.note) body.append(element('p', 'tree-caution', node.note));
    const information = element('details', 'tree-inspect'); information.dataset.key = `inspect:${s.id}`;
    information.open = expansion.get(information.dataset.key) || false;
    information.append(element('summary', '', 'Inspect messages & provenance'));
    information.append(element('p', 'muted', s.context?.parent_thread_id ? `Parent: ${s.context.parent_thread_id} · ${s.parent_evidence || 'recorded relationship'}` : 'No parent recorded for this source.'));
    for (const [label, entry] of [['First recorded user request', overview.first_request], ['Latest recorded assistant reply', overview.latest_reply]]) {
      if (!entry) continue;
      const message = element('details', 'tree-message'); message.dataset.key = `message:${s.id}:${entry.kind}`;
      message.open = expansion.get(message.dataset.key) || false;
      message.append(element('summary', '', label), element('pre', 'tree-content', prettyContent(entry.content)));
      if (entry.clipped) message.append(element('p', 'muted', 'Excerpt limited to 1 KiB. Open the trace for more content.'));
      message.append(provenance(entry)); information.append(message);
    }
    information.append(element('p', 'muted', `Last recorded turn: ${s.metrics?.last_turn || 'unavailable'}. The first request may include inherited context where the source does not identify its boundary; it is not a verified spawn instruction.`));
    body.append(information);
    const open = element('button', 'tree-open', 'Open trace →');
    open.addEventListener('click', () => { mode = 'history'; navigateAgent(s.id); }); body.append(open); details.append(body); item.append(details);
    if (node.children.length) {
      const children = element('ul', 'tree-children');
      // Cap visual indentation, retaining every relationship in a deep tree.
      if (depth >= 4) children.classList.add('tree-flat-indent');
      for (const child of node.children) children.append(branch(child, depth + 1));
      details.append(children);
    }
    return item;
  }
  for (const root of agentForest(currentTree.sessions)) list.append(branch(root, 0));
  pane.append(list);
}

async function loadAgentTree() {
  const id = selected; if (!id || mode !== 'agents') return;
  try {
    const result = await api(`/api/agents?id=${encodeURIComponent(id)}`);
    if (selected !== id || mode !== 'agents') return;
    currentTree = result; renderTimeline();
  } catch (error) {
    if (selected !== id || mode !== 'agents') return;
    currentTree = {error: error.message}; renderTimeline();
  }
}
async function loadLive() {
  const id = selected; if (!id) return;
  const result = await api(`/api/session?id=${encodeURIComponent(id)}`);
  if (id !== selected) return;
  detailedSession = result.session;
  currentLive = result.events; renderHeading(); if (mode === 'live' || mode === 'metrics') renderTimeline();
}

function renderDiagnostics() {
  const total = Object.values(snapshot.diagnostics).reduce((a,b) => a+b, 0);
  $('diagnostic-count').textContent = total;
  if (!$('diagnostics').open) return;
  const body = $('diagnostic-body'); body.replaceChildren();
  for (const root of snapshot.roots) { const row = element('div', 'diagnostic-root'); row.append(element('strong', '', `${root.agent} · ${root.status}`), document.createTextNode(root.path), element('p', '', root.detail || 'Initial source scan in progress.')); body.append(row); }
  for (const [key, count] of Object.entries(snapshot.diagnostics)) { const row = element('div', 'diagnostic-row'); row.append(element('span', '', key), element('strong', '', count)); body.append(row); }
  if (!total) body.append(element('p', '', 'No diagnostic records so far.'));
}

document.querySelector('.sidebar').addEventListener('click', event => { const button = event.target.closest('[data-provider]'); if (!button) return; provider = button.dataset.provider; renderList(); });
$('session-list').addEventListener('click', event => {
  const row = event.target.closest('[data-session]'); if (!row) return;
  if (row.dataset.conversation) {
    const conversation = inventory.bySource.get(row.dataset.session); if (conversation) selectConversation(conversation);
  } else if (row.dataset.session !== selected) selectSession(row.dataset.session);
});
$('inventory-view').addEventListener('change', () => { listMode = $('inventory-view').value; renderList(); });
$('search').addEventListener('input', renderList);
function showUpdatedActivity() {
  renderList();
  if (!selected) return;
  const family = inventory.bySource.get(selected);
  mode = listMode === 'conversations' && (family?.source_count > 1 || family?.main_missing) ? 'agents' : 'live';
  renderTimeline(true);
  if (mode === 'agents') loadAgentTree(); else loadLive().catch(error => notice(error.message));
}
$('window').addEventListener('change', () => {
  if ($('window').value === 'updates') showUpdatedActivity(); else renderList();
});
$('recent-summary').addEventListener('click', () => { provider = 'all'; $('search').value = ''; $('window').value = 'updates'; showUpdatedActivity(); });
$('history-tab').addEventListener('click', () => { mode = 'history'; if (!currentHistory) loadHistory(); else renderTimeline(true); });
$('metrics-tab').addEventListener('click', () => { mode = 'metrics'; renderTimeline(true); });
$('agents-tab').addEventListener('click', () => { mode = 'agents'; renderTimeline(true); loadAgentTree(); });
$('event-type').addEventListener('change', () => renderTimeline(true));
$('trace-search').addEventListener('input', () => renderTimeline(true));
$('live-tab').addEventListener('click', () => { mode = 'live'; renderTimeline(true); loadLive().catch(error => notice(error.message)); });
$('refresh-history').addEventListener('click', loadHistory);
$('diagnostics-button').addEventListener('click', () => { $('diagnostics').showModal(); renderDiagnostics(); });
$('close-diagnostics').addEventListener('click', () => $('diagnostics').close());

async function poll() {
  try {
    snapshot = await api('/api/sessions'); inventory = groupConversations(snapshot.sessions); connected = true; everConnected = true;
    $('connection').textContent = 'Connected · local observer'; $('connection').className = 'connected';
    notice(snapshot.include_content ? '' : 'Metadata-only view. Start AgentView with --include-content to read messages and tool payloads.');
    renderList(); renderHeading(); renderDiagnostics(); tour.maybeStart(); await loadLive(); if (mode === 'metrics') renderTimeline(); if (mode === 'agents') await loadAgentTree();
  } catch (error) {
    connected = false;
    $('connection').textContent = everConnected ? 'Disconnected · retrying' : 'Not connected'; $('connection').className = 'error';
    renderList(); renderHeading();
    if (mode === 'agents') renderTimeline();
    notice(error.message || 'Cannot reach the local observer. Keep agentview serve running.');
  } finally { setTimeout(poll, 1000); }
}
poll();
