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

test('generated HTML and scripts automatically load as inert source in every browser', async ({ page }) => {
  const unexpected = [];
  page.on('request', (request) => {
    if (!request.url().startsWith('http://127.0.0.1:3100/')) unexpected.push(request.url());
  });
  const markup = [
    '<h1 id="untrusted-heading">An example page</h1>',
    '<img src="https://example.com/source-must-not-load" onerror="while(true){}">',
    '<iframe srcdoc="<script>new RTCPeerConnection()<\/script>"></iframe>',
    '<meta http-equiv="refresh" content="0;url=https://example.com/redirect">',
    '<script>while (true) {}<\/script>',
  ].join('\n');
  const runner = await openCodeWithAnswer(page, [
    '```html', markup, '```',
    '```css', 'h1 { color: blue; }', '```',
    '```javascript', 'console.log("embedded-script-must-not-run");', '```',
  ].join('\n'));
  // Use the concrete source element because browsers map labelled pre elements differently.
  await expect(runner.locator('pre.html-source')).toContainText(markup);
  await expect(runner.locator('pre.html-source')).toContainText('h1 { color: blue; }');
  await expect(runner.locator('pre.html-source')).toContainText('embedded-script-must-not-run');
  await expect(page.locator('#runnerNetworkState')).toHaveText('Source only');
  await expect(page.locator('#runCode')).toHaveText('Show source');
  await expect(page.locator('.generated-code-head button').first()).toHaveText('Show source');
  await expect(page.locator('#runnerStatus')).toHaveText('HTML shown as source. Interactive execution is paused.');
  await expect(runner.locator('iframe, img, #untrusted-heading')).toHaveCount(0);
  await expect(page.locator('#runnerResults')).not.toContainText('LOG  embedded-script');
  expect(unexpected).toEqual([]);
});

test('HTML can be edited, stopped and reloaded without executing markup', async ({ page }) => {
  const runner = await openCodeWithAnswer(page, '```html\n<button>original source</button>\n```');
  await expect(runner.locator('pre.html-source')).toContainText('original source');
  await page.locator('#editorTab').click();
  await page.locator('#runnerEditor').fill('<p>edited source</p><script>while(true){}<\/script>');
  await page.locator('#runCode').click();
  await expect(runner.locator('pre.html-source')).toContainText('edited source');
  await page.locator('#stopCode').click();
  await expect(runner.locator('pre.html-source')).toHaveCount(0);
  await page.locator('#editorTab').click();
  await page.locator('#runnerEditor').fill('<p>unrun source</p>');
  await page.locator('#rerunCode').click();
  await expect(runner.locator('pre.html-source')).toContainText('edited source');
  await expect(runner.locator('pre.html-source')).not.toContainText('unrun source');
  await page.getByRole('tab', { name: 'Chat', exact: true }).click();
  await expect(page.locator('#codeRunner')).toBeHidden();
  await expect(page.locator('#input')).toBeEditable();
});

test('switching from source-only HTML to JavaScript still runs console code', async ({ page }) => {
  const runner = await openCodeWithAnswer(page, '```html\n<p>source</p>\n```');
  await expect(runner.locator('pre.html-source')).toBeVisible();
  await page.locator('#editorTab').click();
  await page.locator('#runnerLanguage').selectOption('javascript');
  await expect(page.locator('#runCode')).toHaveText('Run code');
  await page.locator('#runnerEditor').fill('console.log("worker remains available");');
  await page.locator('#runCode').click();
  await expect(runner.getByText('worker remains available', { exact: true })).toBeVisible();
  await expect(page.locator('#runnerStatus')).toHaveText('Run completed. No tests were declared.');
  await expect(page.locator('#runnerNetworkState')).not.toHaveText('Source only');
  await expect(runner.locator('pre.html-source')).toHaveCount(0);
});

test('hosted broker policy forbids all nested frames and external connections', async ({ request }) => {
  const response = await request.get('/client/assets/runner.html');
  expect(response.ok()).toBe(true);
  const policy = response.headers()['content-security-policy'];
  expect(policy).toContain("frame-src 'none'");
  expect(policy).toContain("connect-src 'none'");
  expect(policy).toContain("worker-src blob:");
  expect(response.headers()['connection-allowlist']).toBe('(response-origin);webrtc=block');
});

test('a timed-out worker can be replaced, stopped, and reloaded', async ({ page }) => {
  const runner = await openCodeWithAnswer(page, '```javascript\nwhile (true) {}\n```');
  await expect(page.locator('#runnerStatus')).toHaveText('Stopped at the 2.5 second limit.');
  await page.locator('#editorTab').click();
  await page.locator('#runnerEditor').fill('console.log("recovered-snapshot");');
  await page.locator('#runCode').click();
  await expect(runner.getByText('recovered-snapshot', { exact: true })).toBeVisible();
  await page.locator('#stopCode').click();
  await expect(page.locator('#runnerStatus')).toHaveText('Preview stopped. Reload to run the last snapshot.');
  await expect(runner.getByText('recovered-snapshot', { exact: true })).toHaveCount(0);
  await page.locator('#editorTab').click();
  await page.locator('#runnerEditor').fill('console.log("unrun-edit");');
  await page.locator('#rerunCode').click();
  await expect(runner.getByText('recovered-snapshot', { exact: true })).toBeVisible();
  await expect(runner.getByText('unrun-edit', { exact: true })).toHaveCount(0);
});
