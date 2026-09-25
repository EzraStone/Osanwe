import { test, expect, openClient, loadKey } from './fixtures.mjs';

for (const status of [401, 429, 503]) {
  test(`connection check recovers after HTTP ${status} without automatic retries`, async ({ page }) => {
    let calls = 0;
    await page.route('**/api/providers/check', async route => {
      calls++;
      if (calls === 1) return route.fulfill({ status, contentType: 'text/html', body: '<html>hidden-infrastructure-id</html>' });
      const { provider, model } = route.request().postDataJSON();
      return route.fulfill({ json: { ok: true, provider, model } });
    });
    await openClient(page); await loadKey(page, { close: false });
    await page.locator('#testProviderKey').click();
    await expect(page.locator('#testProviderKey')).toBeEnabled();
    await expect(page.locator('#providerKeyStatus')).not.toContainText('hidden-infrastructure-id');
    await expect(page.locator('#providerKeyStatus')).not.toContainText('Connection verified');
    expect(calls).toBe(1);
    await page.locator('#testProviderKey').click();
    await expect(page.locator('#providerKeyStatus')).toContainText('Connection verified');
    expect(calls).toBe(2);
  });
}

test('a false-success probe response cannot display verified access', async ({ page }) => {
  await page.route('**/api/providers/check', route => route.fulfill({ json: { ok: true, provider: 'wrong', model: 'wrong' } }));
  await openClient(page); await loadKey(page, { close: false });
  await page.locator('#testProviderKey').click();
  await expect(page.locator('#providerKeyStatus')).toContainText('did not verify');
  await expect(page.locator('#testProviderKey')).toBeEnabled();
});
