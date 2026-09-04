import { expect, test } from '@playwright/test';

const providerCatalog = {
  providers: [{ id: 'tokenrouter', label: 'TokenRouter', models: ['z-ai/glm-5.3-free'] }],
};

function normalizedAnswer(text) {
  return [
    `data: ${JSON.stringify({ type: 'content_block_delta', delta: { type: 'text_delta', text } })}\n\n`,
    `data: ${JSON.stringify({ type: 'message_stop' })}\n\n`,
  ].join('');
}

async function openCodeWithAnswer(page, answer) {
  await page.route('**/api/providers', (route) => route.fulfill({ json: providerCatalog }));
  await page.route('**/api/chat', (route) => route.fulfill({
    status: 200,
    contentType: 'text/event-stream',
    headers: { 'cache-control': 'no-store' },
    body: normalizedAnswer(answer),
  }));
  await page.goto('/client');
  await page.getByRole('button', { name: 'Settings', exact: true }).click();
  await page.locator('#providerConsent').check();
  await page.locator('#providerKey').fill('browser-test-provider-key');
  await page.getByRole('button', { name: 'Load for this tab' }).click();
  await page.locator('#closeSettingsIcon').click();
  await page.getByRole('tab', { name: 'Code' }).click();
  await page.locator('#input').fill('Create the requested preview.');
  await page.locator('#send').click();
  await expect(page.locator('#codeRunner')).toBeVisible();
  return page.frameLocator('#runnerPreview');
}

test('generated JavaScript automatically runs in the display pane', async ({ page }) => {
  const runner = await openCodeWithAnswer(page, [
    '```javascript',
    'console.log("Rendered generated code");',
    '```',
  ].join('\n'));
  await expect(runner.getByText('Rendered generated code', { exact: true })).toBeVisible();
  await expect(page.locator('#runnerStatus')).toHaveText('Run completed. No tests were declared.');
});

test('generated HTML runs only when the browser enforces the network boundary', async ({ browser, page }) => {
  const escaped = [];
  page.on('request', (request) => {
    if (!request.url().startsWith('http://127.0.0.1:3100/')) escaped.push(request.url());
  });
  const runner = await openCodeWithAnswer(page, [
    '```html',
    '<!doctype html><html><body>',
    '<p id="network">pending</p><p id="parent">pending</p>',
    '<script>',
    'fetch("https://example.com/should-not-leave").then(() => document.querySelector("#network").textContent="escaped").catch(() => document.querySelector("#network").textContent="blocked");',
    'try { parent.document.body.innerText; document.querySelector("#parent").textContent="escaped"; } catch { document.querySelector("#parent").textContent="blocked"; }',
    '</script></body></html>',
    '```',
  ].join('\n'));
  const browserMajor = Number.parseInt(browser.version().split('.')[0], 10);
  if (browserMajor < 152) {
    await expect(page.locator('#runnerNetworkState')).toHaveText('HTML locked');
    await expect(page.locator('#runnerStatus')).toHaveText('Run completed with errors.');
    expect(escaped).toEqual([]);
    return;
  }
  const preview = runner.frameLocator('iframe.app-preview');
  await expect(preview.locator('#network')).toHaveText('blocked');
  await expect(preview.locator('#parent')).toHaveText('blocked');
  await expect(page.locator('#runnerNetworkState')).toHaveText('Network blocked');
  expect(escaped).toEqual([]);
});
