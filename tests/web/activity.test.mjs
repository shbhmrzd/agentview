import test from 'node:test';
import assert from 'node:assert/strict';
import {sessionActivity, visibleSessions, RECENT_UPDATE_MS} from '../../internal/dashboard/web/helpers.mjs';
import {tourSeen, rememberTour, TOUR_KEY} from '../../internal/dashboard/web/tour.mjs';

const now = Date.parse('2026-11-13T12:00:00Z');
const recent = {id:'main', agent:'codex', project:'agentview', status:'watching', last_activity:new Date(now).toISOString()};
const active = {...recent, last_update_at:new Date(now - 1000).toISOString()};
const activity = (s, connected = true) => sessionActivity(s, connected, now).kind;

test('recent files and newly appended conversation events have distinct labels', () => {
  assert.equal(activity(recent), 'recent-file');
  assert.equal(activity(active), 'new');
  assert.equal(activity({...active, last_activity:'2020-01-01T00:00:00Z'}), 'new');
  assert.equal(activity({...recent, last_activity:'2020-01-01T00:00:00Z'}), 'quiet');
  assert.equal(activity({...active, status:'indexing'}), 'indexing');
  assert.equal(activity({...active, status:'unavailable'}), 'unavailable');
  assert.equal(activity(active, false), 'disconnected');
});

test('new activity expires at two minutes and ignores invalid/future timestamps', () => {
  const session = {...active, last_activity:'2020-01-01T00:00:00Z'};
  assert.equal(activity({...session, last_update_at:new Date(now - RECENT_UPDATE_MS + 1).toISOString()}), 'new');
  assert.equal(activity({...session, last_update_at:new Date(now - RECENT_UPDATE_MS).toISOString()}), 'quiet');
  for (const time of [undefined, '', 'invalid', new Date(now + 1).toISOString()]) {
    assert.equal(activity({...session, last_update_at:time}), 'quiet');
  }
});

test('new activity filter composes with provider and search and excludes unavailable sources', () => {
  const claude = {...active, id:'child', agent:'claude'};
  const unavailable = {...active, id:'missing', status:'unavailable'};
  const sessions = [recent, active, claude, unavailable];
  assert.deepEqual(visibleSessions(sessions, 'all', '', 'updates', now), [active, claude]);
  assert.deepEqual(visibleSessions(sessions, 'codex', 'agentview', 'updates', now), [active]);
  assert.deepEqual(visibleSessions(sessions, 'all', 'missing', 'updates', now), []);
  assert.deepEqual(visibleSessions(sessions, 'all', '', 'updates', now + RECENT_UPDATE_MS), []);
});

test('tour dismissal persists without storing session content and tolerates blocked storage', () => {
  const values = new Map();
  const storage = {getItem:key => values.get(key), setItem:(key,value) => values.set(key,value)};
  assert.equal(tourSeen(storage), false);
  rememberTour(storage);
  assert.equal(tourSeen(storage), true);
  assert.deepEqual([...values], [[TOUR_KEY, 'seen']]);
  const blocked = {getItem(){throw new Error('disabled');}, setItem(){throw new Error('disabled');}};
  assert.doesNotThrow(() => rememberTour(blocked));
  assert.equal(tourSeen(blocked), false);
  assert.doesNotThrow(() => rememberTour(undefined));
});
