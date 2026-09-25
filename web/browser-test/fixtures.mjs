import { test as base, expect } from '@playwright/test';

export const syntheticKey = 'fixture-not-a-real-key';
export const catalog = { providers: [
  { id: 'groq', label: 'Groq', models: ['test-groq', 'test-groq-large'] },
  { id: 'tokenrouter', label: 'TokenRouter', models: ['z-ai/glm-5.3-free'] },
] };
export function answer(text = 'A blue paper boat.') {
  return 'data: ' + JSON.stringify({ type: 'content_block_delta', delta: { type: 'text_delta', text } }) +
    '\n\ndata: {"type":"message_stop"}\n\n';
}
export const test = base.extend({
  offlineProviders: [async ({ page }, use) => {
    const external = [];
    await page.route('**/*', async route => {
      const url = new URL(route.request().url());
      if (url.origin !== 'http://127.0.0.1:3100') {
        external.push(url.origin); return route.abort();
      }
      if (url.pathname === '/api/providers') return route.fulfill({ json: catalog });
      if (url.pathname === '/api/chat') return route.fulfill({ contentType: 'text/event-stream', body: answer() });
      if (url.pathname === '/api/providers/check') {
        const { provider, model } = route.request().postDataJSON();
        return route.fulfill({ json: { ok: true, provider, model } });
      }
      // Unknown API calls must never leak through to a real backend.
      if (url.pathname.startsWith('/api/')) return route.fulfill({ status: 501, json: { error: 'Unmocked test endpoint' } });
      return route.continue();
    });
    await use();
    expect(external).toEqual([]);
  }, { auto: true }],
});
export { expect };
export async function openClient(page) {
  await page.goto('/client');
  await expect(page.locator('#providerSelect option')).toHaveCount(2);
  await expect(page.locator('#modelTrigger')).toBeEnabled();
}
export async function loadKey(page, { close = true, provider = 'groq' } = {}) {
  await page.locator('#settingsBtn').click();
  if (provider !== await page.locator('#providerSelect').inputValue()) {
    await page.locator('#providerSelect').selectOption(provider);
    await expect(page.locator('#providerKeyStatus')).toContainText('Provider changed');
  }
  await page.locator('#providerConsent').check();
  await page.locator('#providerKey').fill(syntheticKey);
  await page.locator('#connectProviderKey').click();
  await expect(page.locator('#testProviderKey')).toBeVisible();
  if (close) await page.locator('#closeSettingsIcon').click();
}
export async function sendPrompt(page, text = 'Invent a blue toy boat.') {
  await page.locator('#input').fill(text);
  await page.locator('#send').click();
}
