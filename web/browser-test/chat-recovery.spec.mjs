import { test, expect, openClient, loadKey, sendPrompt, answer } from './fixtures.mjs';

for (const failure of ['empty', 'truncated', 'edge', 'network']) {
  test(`chat recovers from ${failure} responses without adding failed answers to context`, async ({ page }) => {
    const payloads = [];
    await page.route('**/api/chat', async route => {
      payloads.push(route.request().postDataJSON());
      if (payloads.length > 1) return route.fulfill({ contentType: 'text/event-stream', body: answer('Recovered answer.') });
      if (failure === 'network') return route.abort();
      if (failure === 'edge') return route.fulfill({ status: 429, contentType: 'text/html', body: '<html>private-edge-value</html>' });
      const body = failure === 'empty' ? 'data: {"type":"message_stop"}\n\n' : answer('Incomplete text').replace('data: {"type":"message_stop"}\n\n', '');
      return route.fulfill({ contentType: 'text/event-stream', body });
    });
    await openClient(page); await loadKey(page);
    await sendPrompt(page);
    await expect(page.locator('.turn.err')).toBeVisible();
    await expect(page.locator('#stop')).toBeHidden();
    await expect(page.locator('.turn.err')).not.toContainText('private-edge-value');
    expect(payloads).toHaveLength(1);
    await sendPrompt(page, 'Please try this new prompt.');
    await expect(page.locator('.turn.reply')).toContainText('Recovered answer.');
    expect(payloads[1].messages.filter(item => item.role === 'assistant')).toEqual([]);
  });
}
