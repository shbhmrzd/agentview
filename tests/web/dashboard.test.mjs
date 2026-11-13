import test from 'node:test';
import assert from 'node:assert/strict';
import {visibleSessions, prettyContent, age, relatedSessions, resolveTarget, filterEvents, agentForest} from '../../internal/dashboard/web/helpers.mjs';

const root = {id:'a',agent:'codex',session_id:'root',group_id:'group',project:'agentview',last_activity:'2026-01-01T12:00:00Z',context:{}};
const child = {id:'b',agent:'codex',session_id:'child',group_id:'group',project:'agentview',last_activity:root.last_activity,context:{parent_thread_id:'root',agent_path:'/root/reviewer',name:'Reviewer'}};
const other = {id:'c',agent:'claude',session_id:'root',group_id:'group',project:'unrelated',last_activity:'2020-01-01T00:00:00Z',context:{}};

test('filters recent file activity without interpreting process liveness', () => {
  assert.deepEqual(visibleSessions([root,child,other],'all','agentview','1',Date.parse('2026-01-01T12:30:00Z')), [root,child]);
  assert.deepEqual(visibleSessions([root,child,other],'claude','','all'),[other]);
  assert.equal(age('',Date.now()),'unknown');
});
test('relationships preserve provider boundaries, ancestors and cycles', () => {
  assert.deepEqual(relatedSessions([root,child,other],'b').map(x=>x.id),['a','b']);
  const cyclic = {...root,context:{parent_thread_id:'child'}};
  assert.equal(relatedSessions([cyclic,child],'a').length,2);
  assert.equal(resolveTarget([root,child,other],root,'/root/reviewer'),child);
  assert.equal(resolveTarget([root,child,other],child,'/root'),root);
  assert.equal(resolveTarget([root,child,other],root,'reviewer'),child);
  assert.equal(resolveTarget([root,child,{...child,id:'d'}],root,'Reviewer'),null);
  const many = Array.from({length:10000},(_,i)=>({...child,id:`child-${i}`,session_id:`child-${i}`}));
  assert.equal(relatedSessions([root,...many],'a').length,10001);
});
test('tool wrappers become readable without executing or dropping sibling fields', () => {
  assert.equal(prettyContent('[{"type":"input_text","text":"hello\\nworld"}]'),'hello\nworld');
  assert.equal(prettyContent('{"cmd":"go test ./..."}'),'go test ./...');
  assert.match(prettyContent('{"output":"hello","exit_code":1}'),/exit_code/);
  assert.equal(prettyContent('<script>alert(1)</script>'),'<script>alert(1)</script>');
});
test('trace filters include explicit coordination only', () => {
  const entries=[{kind:'message.user',content:'hello'},{kind:'tool.requested',tool:'send_message',coordination:{target:'child'}},{kind:'tool.requested',tool:'shell',content:'send_message child'}];
  assert.equal(filterEvents(entries,'coordination','child').length,1);
  assert.equal(filterEvents(entries,'message.','hello').length,1);
  assert.equal(filterEvents(entries,'all','missing').length,0);
});

test('agent tree nests explicit parents and separates groups, missing or ambiguous parents', () => {
  const grandchild = {...child,id:'d',session_id:'grandchild',context:{parent_thread_id:'child'}};
  const missing = {...child,id:'e',session_id:'orphan',context:{parent_thread_id:'missing'}};
  const forest = agentForest([root,child,grandchild,other,missing]);
  assert.equal(forest.length,3);
  assert.equal(forest[0].children[0].session.id,'b');
  assert.equal(forest[0].children[0].children[0].session.id,'d');
  assert.match(forest[2].note,/Parent not in this view/);
  const duplicate = agentForest([root,{...root,id:'copy'},child]);
  assert.equal(duplicate.length,3);
  assert.match(duplicate[2].note,/Ambiguous parent/);
});

test('agent tree breaks cycles without losing sources', () => {
  const forest = agentForest([{...root,context:{parent_thread_id:'child'}},child]);
  assert.equal(forest.length,1);
  assert.match(forest[0].note,/Cycle/);
  assert.equal(forest[0].children.length,1);
  const self = agentForest([{...root,context:{parent_thread_id:'root'}}]);
  assert.match(self[0].note,/self-parent/);
});
