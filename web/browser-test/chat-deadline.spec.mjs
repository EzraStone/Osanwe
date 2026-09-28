import { test, expect, openClient, loadKey, sendPrompt, answer } from './fixtures.mjs';

test('a stalled connection times out visibly and the next prompt can succeed', async ({ page }) => {
  let release, calls = 0;
  const gate = new Promise(resolve => { release = resolve; });
  await page.route('**/api/chat', async route => {
    calls++;
    if (calls === 1) await gate;
    try { await route.fulfill({ contentType: 'text/event-stream', body: answer('Connection recovered.') }); } catch { /* aborted request */ }
  });
  await openClient(page); await loadKey(page);
  await page.clock.install();
  await sendPrompt(page);
  await expect.poll(() => calls).toBe(1);
  await page.clock.fastForward(65001);
  await expect(page.locator('.turn.err')).toContainText('timed out before the server answered');
  await expect(page.locator('#stop')).toBeHidden();
  release();
  expect(calls).toBe(1);
  await sendPrompt(page, 'Try a fresh synthetic prompt.');
  await expect(page.locator('.turn.reply')).toContainText('Connection recovered.');
  expect(calls).toBe(2);
});
