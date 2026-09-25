import test from 'node:test';
import assert from 'node:assert/strict';
import { ConnectionCheck } from '../public/client/assets/connection-check.js';

test('new connection checks cancel old owners and ignore their later completion', () => {
  const state = new ConnectionCheck();
  const old = state.start(), next = state.start();
  assert.equal(old.controller.signal.aborted, true);
  assert.equal(state.owns(old), false);
  state.finish(old);
  assert.equal(state.owns(next), true);
  assert.equal(state.cancel(), true);
  assert.equal(next.controller.signal.aborted, true);
  assert.equal(state.cancel(), false);
});

test('connection deadlines abort requests but preserve ownership for a timeout message', async () => {
  const state = new ConnectionCheck(), check = state.start(5);
  await new Promise(resolve => check.controller.signal.addEventListener('abort', resolve));
  assert.equal(check.timedOut, true);
  assert.equal(state.owns(check), true);
  state.finish(check);
  assert.equal(state.owns(check), false);
});
