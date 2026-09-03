import assert from 'node:assert/strict';
import test from 'node:test';

import { RequestCapacity } from '../lib/request-capacity.mjs';

test('request capacity limits concurrent and recent work independently', () => {
  const capacity = new RequestCapacity({ maxConcurrent: 1, maxRequests: 2, windowMs: 1000 });
  const first = capacity.acquire('client', 1000);
  assert.equal(typeof first, 'function');
  assert.equal(capacity.acquire('client', 1000), null);
  first();
  const second = capacity.acquire('client', 1001);
  assert.equal(typeof second, 'function');
  second();
  assert.equal(capacity.acquire('client', 1002), null);
  assert.equal(typeof capacity.acquire('client', 2002), 'function');
});

test('request capacity releases a request only once', () => {
  const capacity = new RequestCapacity({ maxConcurrent: 1, maxRequests: 3 });
  const release = capacity.acquire('client', 1000);
  release();
  release();
  assert.equal(typeof capacity.acquire('client', 1001), 'function');
});

test('request capacity removes expired identities and bounds memory', () => {
  const capacity = new RequestCapacity({ maxClients: 2, windowMs: 1000 });
  capacity.acquire('oldest', 1000)();
  capacity.acquire('newer', 1100)();
  capacity.acquire('third', 1200)();
  assert.equal(capacity.clients.has('oldest'), false);
  assert.equal(capacity.clients.size, 2);
  capacity.prune(2201);
  assert.equal(capacity.clients.size, 0);
});

test('request capacity fails closed when every bounded entry is active', () => {
  const capacity = new RequestCapacity({ maxClients: 1 });
  capacity.acquire('active', 1000);
  assert.equal(capacity.acquire('attacker', 1001), null);
  assert.equal(capacity.clients.has('attacker'), false);
});
