import { test, expect, openClient, loadKey, sendPrompt } from './fixtures.mjs';
test('synthetic browser sessions never depend on a real API key or provider', async ({ page }) => {
  await openClient(page);
  await loadKey(page);
  await sendPrompt(page);
  await expect(page.locator('.turn.reply')).toContainText('A blue paper boat.');
});
