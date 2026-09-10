import { expect, test } from '@playwright/test';
import { createServer } from 'node:http';
import { createSocket } from 'node:dgram';

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

test('unsupported browsers clearly block interactive HTML', async ({ browser, page }) => {
  test.skip(Number.parseInt(browser.version(), 10) >= 152, 'This check requires an unsupported browser.');
  await openCodeWithAnswer(page, '```html\n<h1>must not execute</h1>\n```');
  await expect(page.locator('#runnerNetworkState')).toHaveText('HTML locked');
  await expect(page.locator('#runnerStatus')).toHaveText('Run completed with errors.');
  await expect(page.frameLocator('#runnerPreview').locator('iframe.app-preview')).toHaveCount(0);
});

test('supported browsers run HTML with blocked parent and network access', async ({ browser, page }) => {
  const supported = Number.parseInt(browser.version(), 10) >= 152;
  if (process.env.OSANWE_REQUIRE_INTERACTIVE_HTML === '1') expect(supported).toBe(true);
  test.skip(!supported, 'Interactive HTML was NOT exercised: requires Chromium 152+.');
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
  const preview = runner.frameLocator('iframe.app-preview');
  await expect(preview.locator('#network')).toHaveText('blocked');
  await expect(preview.locator('#parent')).toHaveText('blocked');
  await expect(page.locator('#runnerNetworkState')).toHaveText('Network restricted');
  expect(escaped).toEqual([]);
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

for (const transport of ['http', 'webrtc']) test(`native ${transport} in a fresh frame cannot reach calibrated listeners`, async ({ browser, page }) => {
  test.skip(transport === 'webrtc' && process.env.OSANWE_RTC_CRASH_REPRO !== '1',
    'Known nested-frame browser crash; run with OSANWE_RTC_CRASH_REPRO=1. This boundary is NOT verified.');
  const supported = Number.parseInt(browser.version(), 10) >= 152;
  if (process.env.OSANWE_REQUIRE_INTERACTIVE_HTML === '1') expect(supported).toBe(true);
  test.skip(!supported, 'Native network isolation NOT exercised: requires Chromium 152+.');
  let httpHits = 0;
  let udpHits = 0;
  const server = createServer((request, response) => {
    if (request.url !== '/calibration') httpHits++;
    response.setHeader('Access-Control-Allow-Origin', '*');
    response.setHeader('Content-Type', 'text/html');
    response.end('<!doctype html><title>Local canary</title>');
  });
  const socket = createSocket('udp4');
  socket.on('message', () => udpHits++);
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  await new Promise((resolve) => socket.bind(0, '127.0.0.1', resolve));
  const httpUrl = `http://127.0.0.1:${server.address().port}`;
  const stunUrl = `stun:127.0.0.1:${socket.address().port}`;
  const control = await browser.newPage();
  try {
    await control.goto(httpUrl + '/calibration');
    await control.evaluate(async ({ httpUrl, stunUrl }) => {
      await fetch(httpUrl + '/probe');
      const peer = new RTCPeerConnection({ iceServers: [{ urls: stunUrl }] });
      peer.createDataChannel('calibration');
      await peer.setLocalDescription(await peer.createOffer());
      await new Promise((resolve) => setTimeout(resolve, 1500));
      peer.close();
    }, { httpUrl, stunUrl });
    expect(httpHits).toBeGreaterThan(0);
    expect(udpHits).toBeGreaterThan(0);
    await control.close();
    httpHits = 0;
    udpHits = 0;
    const child = `<script>(async () => {
      const native = /native code/.test(Function.prototype.toString.call(fetch));
      let outcome = 'unexpected';
      try { await fetch(${JSON.stringify(httpUrl + '/escape')}); outcome = 'escaped'; } catch { outcome = 'blocked'; }
      let peer;
      try {
        if (${transport === 'webrtc'}) {
          peer = new RTCPeerConnection({ iceServers: [{ urls: ${JSON.stringify(stunUrl)} }] });
          peer.createDataChannel('preview');
          await Promise.race([peer.setLocalDescription(await peer.createOffer()), new Promise(r => setTimeout(r, 1000))]);
        }
      } catch {}
      await new Promise(r => setTimeout(r, 1500));
      if (peer) peer.close();
      parent.postMessage({ probe: true, native, outcome }, '*');
    })();<\/script>`;
    const runner = await openCodeWithAnswer(page, '```html\n' + `<p id="result">pending</p><script>
      const child = document.createElement('iframe');
      child.srcdoc = ${JSON.stringify(child).replaceAll('</script>', '<\\/script>')};
      window.addEventListener('message', event => {
        if (event.source === child.contentWindow && event.data?.probe) {
          document.querySelector('#result').textContent = event.data.native + ':' + event.data.outcome;
        }
      });
      document.body.appendChild(child);
    <\/script>` + '\n```');
    await expect(runner.frameLocator('.app-preview').locator('#result')).toHaveText('true:blocked', { timeout: 8000 });
    expect(httpHits).toBe(0);
    expect(udpHits).toBe(0);
  } finally {
    await control.close();
    await page.close();
    await new Promise((resolve) => server.close(resolve));
    await new Promise((resolve) => socket.close(resolve));
  }
});
