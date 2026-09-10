import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import test from 'node:test';

function verify(extraEnv = {}) {
  const result = spawnSync(process.execPath, ['--import', './test/fixtures/free-verification-fetch.mjs', './scripts/verify-free-tokenrouter.mjs'], {
    input: 'synthetic-private-key\n', encoding: 'utf8', timeout: 10_000,
    env: { ...process.env, TEST_PAID_PRICE: '', TEST_EMPTY_PROBE: '', ...extraEnv },
  });
  assert.doesNotMatch(result.stdout + result.stderr, /synthetic-private-key|synthetic-private-output/);
  return { status: result.status, report: JSON.parse(result.stdout) };
}

test('free verification exercises six bounded cases without retaining keys or replies', () => {
  const { status, report } = verify();
  assert.equal(status, 0);
  assert.equal(report.passed, true);
  assert.equal(report.requestsSent, 6);
  assert.equal(report.results.find(r => r.name === 'long_answer_cancel').clientCancelled, true);
});

test('free verification stops before inference when zero pricing cannot be verified', () => {
  const { status, report } = verify({ TEST_PAID_PRICE: '1' });
  assert.equal(status, 1);
  assert.equal(report.requestsSent, 0);
});

test('free verification stops after an empty direct probe without retries or fallback', () => {
  const { status, report } = verify({ TEST_EMPTY_PROBE: '1' });
  assert.equal(status, 1);
  assert.equal(report.requestsSent, 1);
  assert.equal(report.passed, false);
});
