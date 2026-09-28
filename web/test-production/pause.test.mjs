import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { access } from 'node:fs/promises';
import { createServer } from 'node:net';
import { fileURLToPath } from 'node:url';
import { once } from 'node:events';
import test from 'node:test';

const root = fileURLToPath(new URL('../', import.meta.url));
const cli = fileURLToPath(new URL('../node_modules/next/dist/bin/next', import.meta.url));

async function pausedServer(t, setting) {
  await access(new URL('../.next/BUILD_ID', import.meta.url));
  const reservation = createServer();
  await new Promise(resolve => reservation.listen(0, '127.0.0.1', resolve));
  const port = reservation.address().port;
  await new Promise(resolve => reservation.close(resolve));
  const child = spawn(process.execPath, [cli, 'start', '--hostname', '127.0.0.1', '--port', String(port)], {
    cwd: root, windowsHide: true, stdio: ['ignore', 'pipe', 'ignore'],
    env: { ...process.env, NODE_ENV: 'production', NEXT_TELEMETRY_DISABLED: '1', OSANWE_HOSTED_API_PAUSED: setting },
  });
  t.after(async () => {
    if (child.exitCode !== null || child.signalCode !== null) return;
    const exited = once(child, 'exit');
    child.kill();
    const force = setTimeout(() => child.kill('SIGKILL'), 3000);
    try { await exited; } finally { clearTimeout(force); }
  });
  await new Promise((resolve, reject) => {
    let output = '';
    const timer = setTimeout(() => finish(new Error('Local production server did not become ready.')), 30000);
    const finish = error => {
      clearTimeout(timer); child.off('error', onError); child.off('exit', onExit); child.stdout.off('data', onData);
      if (error) reject(error); else resolve();
    };
    const onError = () => finish(new Error('Local production server could not start.'));
    const onExit = () => finish(new Error('Local production server exited before becoming ready.'));
    const onData = chunk => { output = (output + chunk).slice(-4096); if (output.includes('Ready in')) finish(); };
    child.on('error', onError); child.on('exit', onExit); child.stdout.on('data', onData);
    child.stdout.resume();
  });
  return `http://127.0.0.1:${port}`;
}

for (const setting of ['1', 'misspelled-pause']) {
  test(`built application fails closed with pause setting ${setting}`, { timeout: 60000 }, async t => {
    const origin = await pausedServer(t, setting);
    for (const path of ['/api/chat', '/api/providers/check']) {
      // No key, and deliberately invalid JSON: a broken pause cannot reach a provider.
      const response = await fetch(origin + path, {
        method: 'POST', headers: { 'content-type': 'application/json', origin }, body: '{',
        redirect: 'error', signal: AbortSignal.timeout(15000),
      });
      assert.equal(response.status, 503);
      assert.match(response.headers.get('cache-control'), /no-store/);
      assert.equal(response.headers.get('retry-after'), '300');
      const value = await response.json();
      assert.equal(value.error.code, 'hosted_api_paused');
      assert.equal(value.error.retryable, false);
    }
    for (const path of ['/client', '/api/providers']) {
      const response = await fetch(origin + path, { signal: AbortSignal.timeout(15000) });
      assert.equal(response.status, 200);
      await response.body.cancel();
    }
  });
}
