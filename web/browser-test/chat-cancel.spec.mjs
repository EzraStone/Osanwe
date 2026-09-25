import { test, expect, openClient, loadKey, sendPrompt, answer } from './fixtures.mjs';

for (const action of ['stop', 'new', 'code', 'forget']) {
  test(`${action} cancels a pending chat and leaves controls usable`, async ({ page }) => {
    let release, requests = 0;
    const gate = new Promise(resolve => { release = resolve; });
    await page.route('**/api/chat', async route => {
      requests++;
      if (requests === 1) await gate;
      try { await route.fulfill({ contentType: 'text/event-stream', body: answer() }); } catch { /* stopped fetch */ }
    });
    await openClient(page); await loadKey(page); await sendPrompt(page);
    await expect.poll(() => requests).toBe(1);
    if (action === 'stop') await page.locator('#stop').click();
    if (action === 'new') await page.locator('#newBtn').click();
    if (action === 'code') await page.locator('#codeTab').click();
    if (action === 'forget') {
      await page.locator('#settingsBtn').click();
      await page.locator('#forgetProviderKey').click();
      await page.locator('#closeSettingsIcon').click();
    }
    await expect(page.locator('#stop')).toBeHidden();
    release();
    if (action === 'forget') { await expect(page.locator('#input')).toBeDisabled(); return; }
    if (action === 'new' || action === 'code') await expect(page.locator('.turn')).toHaveCount(0);
    if (action === 'stop') await expect(page.locator('.turn.reply')).toContainText('Stopped');
    await sendPrompt(page, 'A fresh request after cancellation.');
    await expect(page.locator('.turn.reply').last()).toContainText('A blue paper boat.');
  });
}
