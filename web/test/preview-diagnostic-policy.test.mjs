import assert from 'node:assert/strict';
import test from 'node:test';
import { diagnosticVerdict, safeExceptionName } from '../scripts/preview-diagnostic-policy.mjs';

const rtc = { protocol: 'rtc', scenario: 'policy-data', stage: 'completed', packets: 0 };
const http = { protocol: 'http', scenario: 'policy-data', stage: 'rejected', requests: 0, exceptionName: 'TypeError', connectBlocked: true };

test('each unrestricted control must complete and actually reach its listener', () => {
  for (const [protocol, key] of [['rtc', 'packets'], ['http', 'requests']]) {
    const control = { protocol, scenario: 'control', stage: 'completed', [key]: 1 };
    assert.equal(diagnosticVerdict(control).passed, true);
    for (const patch of [{ [key]: 0 }, { [key]: -1 }, { [key]: undefined }, { stage: 'rejected' }, { stage: 'starting' }, { crashed: true }, { interrupted: true }]) {
      assert.equal(diagnosticVerdict({ ...control, ...patch }).passed, false);
    }
  }
});

test('no protected result can pass without its own protocol calibration', () => {
  for (const result of [rtc, http]) {
    assert.equal(diagnosticVerdict(result).reason, 'uncalibrated-control');
    assert.equal(diagnosticVerdict(result, true).passed, true);
  }
});

test('zero traffic cannot turn crashes, incomplete runs or RTC exceptions into passes', () => {
  assert.equal(diagnosticVerdict({ ...rtc, crashed: true }, true).outcome, 'crashed');
  assert.equal(diagnosticVerdict({ ...rtc, interrupted: true }, true).outcome, 'incomplete');
  assert.equal(diagnosticVerdict({ ...rtc, stage: 'starting' }, true).outcome, 'incomplete');
  for (const exceptionName of ['Error', 'TypeError', 'SecurityError', 'TimeoutError']) {
    const verdict = diagnosticVerdict({ ...rtc, stage: 'rejected', exceptionName }, true);
    assert.equal(verdict.outcome, 'rejected');
    assert.equal(verdict.passed, false);
  }
  assert.equal(diagnosticVerdict({ ...rtc, packets: 1 }, true).passed, false);
});

test('HTTP denial requires the expected rejection, a matching CSP event and zero requests', () => {
  assert.equal(diagnosticVerdict(http, true).reason, 'verified-connect-src-denial');
  for (const patch of [{ exceptionName: 'Error' }, { exceptionName: 'TimeoutError' }, { connectBlocked: false }, { stage: 'completed' }, { requests: 1 }, { crashed: true }, { interrupted: true }]) {
    assert.equal(diagnosticVerdict({ ...http, ...patch }, true).passed, false);
  }
});

test('diagnostic errors expose only fixed exception names, never arbitrary content', () => {
  assert.equal(safeExceptionName(new TypeError('synthetic-secret')), 'TypeError');
  assert.equal(safeExceptionName({ name: 'synthetic-secret', message: 'synthetic-secret' }), 'UnknownError');
  assert.equal(safeExceptionName(null), 'UnknownError');
  assert.equal(safeExceptionName({ get name() { throw new Error('synthetic-secret'); } }), 'UnknownError');
  let reads = 0;
  assert.equal(safeExceptionName({ get name() { return ++reads === 1 ? 'TypeError' : 'synthetic-secret'; } }), 'TypeError');
  assert.equal(reads, 1);
  assert.equal(diagnosticVerdict({ protocol: 'unknown' }, true).passed, false);
});
