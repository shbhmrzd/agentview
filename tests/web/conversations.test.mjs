import test from 'node:test';
import assert from 'node:assert/strict';
import {groupConversations, visibleConversations, conversationActivity} from '../../internal/dashboard/web/conversations.mjs';
import {relatedSessions, agentForest} from '../../internal/dashboard/web/helpers.mjs';

const now = Date.parse('2026-11-13T12:00:00Z');
const source = (id, context = {}, extra = {}) => ({id, session_id:id, agent:'codex', project:'agentview', status:'watching', last_activity:'2020-01-01T00:00:00Z', context, ...extra});
const root = source('root', {}, {title:'Build a conversation browser'});
const child = source('child', {parent_thread_id:'root', name:'Reviewer'});
const grandchild = source('grandchild', {parent_thread_id:'child'});

test('one conversation contains the main agent and descendants, not unrelated chats in the same project', () => {
  const other = source('other');
  const {conversations, bySource} = groupConversations([grandchild, other, child, root]);
  assert.equal(conversations.length, 2);
  const family = bySource.get('child');
  assert.equal(family, bySource.get('grandchild'));
  assert.equal(family.primary, root);
  assert.equal(family.source_count, 3);
  assert.equal(family.child_count, 2);
  assert.equal(family.title, root.title);
  assert.notEqual(family, bySource.get('other'));
  assert.equal(groupConversations([root, {...child, agent:'claude'}]).conversations.length, 2);
});

test('group IDs join related files without inventing parent edges; forks stay separate', () => {
  const grouped = source('member', {}, {group_id:'root'});
  const fork = source('fork', {forked_from_id:'root'});
  const {conversations, bySource} = groupConversations([root, grouped, fork]);
  assert.equal(conversations.length, 2);
  assert.equal(bySource.get('member').primary, root);
  assert.equal(agentForest([root, grouped]).length, 2);
  assert.deepEqual(relatedSessions([root, grouped, fork], 'member').map(s => s.id), ['member', 'root']);
});

test('missing parents keep siblings together and accept the main source when discovered later', () => {
  const sibling = source('sibling', {parent_thread_id:'root'});
  const before = groupConversations([child, sibling]).conversations[0];
  assert.equal(before.main_missing, true);
  assert.equal(before.conversation_id, 'root');
  assert.match(before.title, /Conversation root/);
  const after = groupConversations([child, sibling, root]).conversations[0];
  assert.equal(after.id, before.id);
  assert.equal(after.main_missing, false);
  assert.equal(after.primary, root);
  assert.equal(relatedSessions([child, sibling], 'child').length, 2);
});

test('duplicate exports stay inspectable; identity-pending files and cyclic metadata remain bounded', () => {
  const copy = {...root, id:'copy', status:'unavailable'};
  const {conversations} = groupConversations([root, copy]);
  assert.equal(conversations.length, 1);
  assert.equal(conversations[0].source_count, 2);
  assert.equal(conversations[0].child_count, 0);
  assert.equal(conversations[0].primary, root);
  assert.equal(relatedSessions([root, copy], 'copy').length, 2);
  assert.equal(groupConversations([source('a', {}, {session_id:''}), source('b', {}, {session_id:''})]).conversations.length, 2);
  const cycle = groupConversations([{...root, context:{parent_thread_id:'child'}}, child]);
  assert.equal(cycle.conversations.length, 1);
  assert.equal(cycle.conversations[0].main_missing, true);
});

test('Claude child sources sharing the native session ID nest under the main source', () => {
  const main = {...root, agent:'claude'};
  const kids = ['reviewer', 'tester'].map(id => source(id, {parent_thread_id:'root', agent_id:id}, {agent:'claude', session_id:'root'}));
  const row = groupConversations([kids[0], main, kids[1]]).conversations[0];
  assert.equal(row.primary, main);
  assert.equal(row.source_count, 3);
  const forest = agentForest([kids[0], main, kids[1]]);
  assert.equal(forest.length, 1);
  assert.equal(forest[0].session, main);
  assert.equal(forest[0].children.length, 2);
});

test('child updates bring an old main conversation into activity filters and sorting', () => {
  const active = {...child, last_activity:new Date(now).toISOString(), last_update_at:new Date(now - 1000).toISOString()};
  const other = source('other', {}, {last_activity:new Date(now - 5000).toISOString()});
  const rows = groupConversations([root, other, active]).conversations;
  assert.equal(rows[0].primary, root);
  assert.equal(conversationActivity(rows[0], true, now).kind, 'new');
  assert.deepEqual(visibleConversations(rows, 'codex', 'reviewer', '1', now), [rows[0]]);
  assert.deepEqual(visibleConversations(rows, 'all', 'child', 'updates', now), [rows[0]]);
  assert.deepEqual(visibleConversations(rows, 'claude', '', 'updates', now), []);
  assert.equal(visibleConversations(rows, 'all', '', 'updates', now + 120000).length, 0);
  assert.equal(conversationActivity(rows[0], false, now).kind, 'disconnected');
  const unavailable = groupConversations([root, {...active, status:'unavailable'}]).conversations[0];
  assert.equal(conversationActivity(unavailable, true, now).kind, 'quiet');
  assert.equal(visibleConversations([unavailable], 'all', '', 'updates', now).length, 0);
});

test('titles, child IDs, project paths, and agent names are searchable without splitting the family', () => {
  const rows = groupConversations([root, {...child, path:'/logs/review.jsonl', context:{...child.context, agent_path:'/root/reviewer', agent_id:'review-123'}}]).conversations;
  for (const query of ['CONVERSATION browser', 'reviewer', 'child', '/logs/review', 'review-123', 'agentview']) {
    assert.equal(visibleConversations(rows, 'all', query, 'all', now).length, 1, query);
  }
  assert.equal(visibleConversations(rows, 'all', 'missing', 'all', now).length, 0);
});

test('large families build an index and preserve selection membership without pairwise scans', () => {
  const children = Array.from({length:10000}, (_, i) => source(`child-${i}`, {parent_thread_id:'root'}));
  const {conversations, bySource} = groupConversations([...children, root]);
  assert.equal(conversations.length, 1);
  assert.equal(conversations[0].source_count, 10001);
  assert.equal(bySource.get('child-9999').primary, root);
  assert.equal(relatedSessions([...children, root], 'child-9999').length, 10001);
});
