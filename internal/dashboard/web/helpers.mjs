export function visibleSessions(sessions, provider, query, hours, now = Date.now()) {
  const needle = query.trim().toLowerCase();
  return sessions.filter(s => (provider === 'all' || s.agent === provider) &&
    (hours === 'all' || (hours === 'updates' ? sessionActivity(s, true, now).kind === 'new' : Date.parse(s.last_activity) >= now - Number(hours) * 3600000)) &&
    (!needle || [s.title, s.project, s.session_id, s.path, s.directory, s.agent, s.context?.agent_id, s.context?.agent_path, s.context?.name].some(v => (v || '').toLowerCase().includes(needle))));
}

export const RECENT_UPDATE_MS = 2 * 60 * 1000;
export function sessionActivity(session, connected = true, now = Date.now()) {
  if (!connected) return {kind: 'disconnected', label: 'Observer disconnected', detail: 'Reconnect the dashboard to observe further activity.'};
  if (session.status === 'unavailable') return {kind: 'unavailable', label: 'Source unavailable', detail: 'The transcript cannot currently be read.'};
  if (session.status === 'indexing') return {kind: 'indexing', label: 'Indexing history', detail: 'Reading earlier records; this is not live agent activity.'};
  const elapsed = now - Date.parse(session.last_update_at);
  if (elapsed >= 0 && elapsed < RECENT_UPDATE_MS) return {kind: 'new', label: 'New activity', detail: `A new message, tool or turn event was observed ${age(session.last_update_at, now)}. This does not confirm process liveness.`};
  const fileElapsed = now - Date.parse(session.last_activity);
  if (fileElapsed >= 0 && fileElapsed < RECENT_UPDATE_MS) return {kind: 'recent-file', label: 'Recent file activity', detail: 'The source was recently modified or discovered. No recent new conversation event has been observed.'};
  return {kind: 'quiet', label: 'No recent updates', detail: 'No new conversation event observed in the last 2 minutes. The agent may still be working or waiting.'};
}
export function prettyContent(content, depth = 0) {
  if (depth > 4 || typeof content !== 'string') return String(content ?? '');
  let data;
  try { data = JSON.parse(content); } catch { return content; }
  if (typeof data === 'string') return prettyContent(data, depth + 1);
  if (Array.isArray(data) && data.length && data.every(x => x && typeof x.text === 'string')) return data.map(x => prettyContent(x.text, depth + 1)).join('\n\n');
  if (data && typeof data === 'object' && !Array.isArray(data)) {
    // Decode a payload only when no sibling information would be hidden.
    const keys = Object.keys(data);
    if (keys.length === 1 && ['cmd', 'command', 'output', 'text'].includes(keys[0]) && typeof data[keys[0]] === 'string') return prettyContent(data[keys[0]], depth + 1);
  }
  return JSON.stringify(data, null, 2);
}
export function age(when, now = Date.now()) {
  const minutes = Math.max(0, Math.floor((now - Date.parse(when)) / 60000));
  if (!Number.isFinite(minutes)) return 'unknown';
  if (minutes < 1) return 'just now';
  if (minutes < 60) return `${minutes}m ago`;
  if (minutes < 1440) return `${Math.floor(minutes / 60)}h ago`;
  return `${Math.floor(minutes / 1440)}d ago`;
}

export function relatedSessions(sessions, selectedID) {
  const selected = sessions.find(s => s.id === selectedID);
  if (!selected) return [];
  const index = new Map();
  const identifiers = s => [s.session_id, s.group_id, s.context?.parent_thread_id].filter(Boolean);
  for (const s of sessions) {
    if (s.agent !== selected.agent) continue;
    for (const key of identifiers(s)) {
      if (!index.has(key)) index.set(key, []);
      index.get(key).push(s);
    }
  }
  const queued = new Set([selected.id]), expanded = new Set(), queue = [selected];
  for (let i = 0; i < queue.length; i++) {
    for (const key of identifiers(queue[i])) {
      if (expanded.has(key)) continue;
      expanded.add(key);
      for (const s of index.get(key) || []) {
        if (!queued.has(s.id)) { queued.add(s.id); queue.push(s); }
      }
    }
  }
  return queue.sort((a,b) => Number(!!a.context?.parent_thread_id) - Number(!!b.context?.parent_thread_id) || a.id.localeCompare(b.id));
}

export function resolveTarget(sessions, current, target) {
  if (!target) return null;
  const matches = sessions.filter(s => s.agent === current?.agent && [s.session_id, s.context?.agent_id, s.context?.agent_path, s.context?.name].includes(target));
  if (matches.length) return matches.length === 1 ? matches[0] : null;
  const family = relatedSessions(sessions, current?.id);
  // Native Codex logs can omit the root path while child metadata records
  // both /root/child and its parent thread ID. Resolve that exact parent edge.
  const parentIDs = new Set(family.filter(s => s.context?.agent_path?.slice(0,s.context.agent_path.lastIndexOf('/')) === target).map(s => s.context?.parent_thread_id).filter(Boolean));
  const candidates = family.filter(s => parentIDs.has(s.session_id) || (!target.includes('/') && s.context?.agent_path?.split('/').pop() === target));
  return candidates.length === 1 ? candidates[0] : null;
}

export function filterEvents(events, kind, query) {
  const needle = query.trim().toLowerCase();
  return events.filter(e => (kind === 'all' || (kind === 'coordination' ? !!e.coordination : e.kind.startsWith(kind))) &&
    (!needle || [e.content, e.tool, e.call_id, e.coordination?.target].some(v => (v || '').toLowerCase().includes(needle))));
}

// Group IDs collect related sources; only an unambiguous parent ID nests them.
// The endpoint bounds this to 100 nodes. Never silently invent an edge.
export function agentForest(sessions) {
  const nodes = sessions.map(session => ({session, children: [], parent: null, note: ''}));
  const native = new Map();
  for (const node of nodes) {
    const key = `${node.session.agent}:${node.session.session_id}`;
    if (!native.has(key)) native.set(key, []);
    native.get(key).push(node);
  }
  for (const node of nodes) {
    const parentID = node.session.context?.parent_thread_id;
    if (!parentID) continue;
    let matches = native.get(`${node.session.agent}:${parentID}`) || [];
    // Claude child files can retain the main conversation's session ID.
    // Their layout-derived parent identifies them as children, not candidates
    // for the main source that owns that same ID.
    if (node.session.agent === 'claude') matches = matches.filter(candidate => candidate.session.context?.parent_thread_id !== parentID);
    if (matches.length === 1 && matches[0] !== node) node.parent = matches[0];
    else node.note = matches.length > 1 ? `Ambiguous parent source: ${parentID}` : matches[0] === node ? 'Invalid self-parent link' : `Parent not in this view: ${parentID}`;
  }
  for (const node of nodes) {
    const seen = new Set([node]);
    for (let parent = node.parent; parent; parent = parent.parent) {
      if (seen.has(parent)) { node.parent = null; node.note = 'Cycle in recorded parent links'; break; }
      seen.add(parent);
    }
  }
  const roots = [];
  for (const node of nodes) { if (node.parent) node.parent.children.push(node); else roots.push(node); }
  return roots;
}
