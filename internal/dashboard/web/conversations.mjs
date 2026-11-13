import {sessionActivity} from './helpers.mjs';

const timestamp = value => Date.parse(value) || 0;
export const shortID = value => !value ? 'identity pending' : value.length > 16 ? `${value.slice(0,8)}…${value.slice(-4)}` : value;
const searchFields = s => [s.title, s.project, s.session_id, s.path, s.directory, s.agent, s.context?.agent_path, s.context?.agent_id, s.context?.name, s.context?.parent_thread_id];

// Linear-size union/find index: only explicit provider-scoped identifiers
// connect sources. Project paths, titles and fork provenance never join chats.
export function groupConversations(sessions) {
  const parent = new Map(), rank = new Map();
  const key = (provider, kind, id) => JSON.stringify([provider, kind, id]);
  function find(id) {
    if (!parent.has(id)) { parent.set(id,id); rank.set(id,0); return id; }
    let root = id;
    while (parent.get(root) !== root) root = parent.get(root);
    while (id !== root) { const next = parent.get(id); parent.set(id,root); id = next; }
    return root;
  }
  function join(a,b) {
    let left = find(a), right = find(b); if (left === right) return;
    if (rank.get(left) < rank.get(right)) [left,right] = [right,left];
    parent.set(right,left);
    if (rank.get(left) === rank.get(right)) rank.set(left,rank.get(left)+1);
  }
  for (const s of sessions) {
    const source = key(s.agent,'source',s.id); find(source);
    for (const id of [s.session_id, s.group_id, s.context?.parent_thread_id]) if (id) join(source,key(s.agent,'native',id));
  }
  const buckets = new Map();
  for (const s of sessions) {
    const group = find(key(s.agent,'source',s.id));
    if (!buckets.has(group)) buckets.set(group,[]);
    buckets.get(group).push(s);
  }
  const conversations = [], bySource = new Map();
  for (const members of buckets.values()) {
    // Prefer a readable main source, with deterministic tie breaking for
    // duplicate exports. Selecting a child does not change the representative.
    const stable = [...members].sort((a,b) => Number(a.status === 'unavailable')-Number(b.status === 'unavailable') || a.id.localeCompare(b.id));
    const roots = stable.filter(s => !s.context?.parent_thread_id);
    const referenced = new Set(members.flatMap(s => [s.group_id,s.context?.parent_thread_id]).filter(Boolean));
    const main = roots.find(s => s.session_id && referenced.has(s.session_id)) || roots[0] || null;
    const primary = main || stable[0];
    const nativeIDs = new Set(members.map(s => s.session_id).filter(Boolean));
    const missingParents = [...new Set(members.map(s => s.context?.parent_thread_id).filter(id => id && !nativeIDs.has(id)))].sort();
    const conversationID = main?.session_id || missingParents[0] || [...referenced].sort()[0] || primary.session_id || primary.id;
    const row = {
      id: key(primary.agent,'conversation',conversationID), agent: primary.agent, conversation_id: conversationID,
      main, primary, members, source_count: members.length,
      child_count: members.filter(s => !!s.context?.parent_thread_id).length,
      title: main?.title || `Conversation ${shortID(conversationID)}`,
      title_from_request: !!main?.title, project: primary.project,
      last_activity: members.reduce((latest,s) => timestamp(s.last_activity)>timestamp(latest)?s.last_activity:latest,''),
      main_missing: !main,
    };
    conversations.push(row);
    for (const s of members) bySource.set(s.id,row);
  }
  conversations.sort((a,b) => timestamp(b.last_activity)-timestamp(a.last_activity) || a.id.localeCompare(b.id));
  return {conversations, bySource};
}

export function conversationActivity(conversation, connected = true, now = Date.now()) {
  const observations = conversation.members.map(s => sessionActivity(s,connected,now));
  const priority = ['disconnected','new','recent-file','indexing','quiet','unavailable'];
  for (const kind of priority) {
    const matches = observations.filter(s => s.kind === kind);
    if (!matches.length) continue;
    const activity = {...matches[0]};
    if (kind === 'new') activity.detail = `New conversation activity observed in ${matches.length} source${matches.length === 1 ? '' : 's'} in the last 2 minutes. Open Agent tree to find the active agent. Process state is unknown.`;
    return activity;
  }
  return {kind:'quiet',label:'No recent updates',detail:'No recent conversation activity observed.'};
}

export function visibleConversations(conversations, provider, query, hours, now = Date.now()) {
  const needle = query.trim().toLowerCase();
  return conversations.filter(row => (provider === 'all' || row.agent === provider) &&
    (hours === 'all' || (hours === 'updates' ? conversationActivity(row,true,now).kind === 'new' : timestamp(row.last_activity) >= now - Number(hours)*3600000)) &&
    (!needle || [row.title,row.conversation_id,...row.members.flatMap(searchFields)].some(value => (value || '').toLowerCase().includes(needle))));
}
