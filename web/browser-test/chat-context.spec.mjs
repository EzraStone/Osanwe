import { test, expect, openClient, loadKey, sendPrompt, answer } from './fixtures.mjs';

test('follow-ups include completed context without leaking settings into messages', async ({ page }) => {
  const payloads = [];
  await page.route('**/api/chat', route => {
    payloads.push(route.request().postDataJSON());
    return route.fulfill({ contentType: 'text/event-stream', body: answer() });
  });
  await openClient(page); await loadKey(page);
  await sendPrompt(page, 'Invent a blue toy boat.');
  await expect(page.locator('#stop')).toBeHidden();
  await sendPrompt(page, 'What color was it?');
  await expect(page.locator('.turn.reply')).toHaveCount(2);
  expect(payloads[1].messages).toEqual([
    { role: 'user', content: 'Invent a blue toy boat.' },
    { role: 'assistant', content: 'A blue paper boat.' },
    { role: 'user', content: 'What color was it?' },
  ]);
  expect(JSON.stringify(payloads)).not.toContain('fixture-not-a-real-key');
});

test('New starts an empty context without silently changing provider access', async ({ page }) => {
  await openClient(page); await loadKey(page);
  await sendPrompt(page, 'old synthetic prompt');
  await expect(page.locator('#stop')).toBeHidden();
  await page.locator('#newBtn').click();
  await expect(page.locator('.turn')).toHaveCount(0);
  const request = page.waitForRequest('**/api/chat');
  await sendPrompt(page, 'new synthetic prompt');
  expect((await request).postDataJSON().messages).toEqual([{ role: 'user', content: 'new synthetic prompt' }]);
});
