import assert from 'node:assert/strict';
import test from 'node:test';

import { FOLLOW_DISTANCE_PX, isNearConversationEnd } from '../public/client/assets/follow-scroll.js';

test('conversation follows updates while the reader is at the end', () => {
  assert.equal(isNearConversationEnd({ scrollHeight: 1000, scrollTop: 400, clientHeight: 600 }), true);
  assert.equal(
    isNearConversationEnd({ scrollHeight: 1000, scrollTop: 400 - FOLLOW_DISTANCE_PX, clientHeight: 600 }),
    true,
  );
});

test('conversation stops following after the reader intentionally scrolls upward', () => {
  assert.equal(isNearConversationEnd({ scrollHeight: 1000, scrollTop: 300, clientHeight: 600 }), false);
});
