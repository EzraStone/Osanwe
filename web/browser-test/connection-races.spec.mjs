import { test, expect, openClient, loadKey } from './fixtures.mjs';

for (const action of ['forget', 'provider', 'model', 'cancel', 'catalog']) {
  test(`a late connection check cannot overwrite the ${action} action`, async ({ page }) => {
    let release, observed = false;
    const gate = new Promise(resolve => { release = resolve; });
    let responded;
    const responseDone = new Promise(resolve => { responded = resolve; });
    await page.route('**/api/providers/check', async route => {
      observed = true;
      const { provider, model } = route.request().postDataJSON();
      await gate;
      try { await route.fulfill({ json: { ok: true, provider, model } }); } catch { /* cancelled transport */ }
      responded();
    });
    await openClient(page); await loadKey(page, { close: false });
    await page.locator('#testProviderKey').click();
    await expect.poll(() => observed).toBe(true);
    if (action === 'forget') await page.locator('#forgetProviderKey').click();
    if (action === 'provider') await page.locator('#providerSelect').selectOption('tokenrouter');
    if (action === 'model') {
      await page.locator('#providerModel').fill('another-model');
      await page.locator('#useProviderModel').click();
    }
    if (action === 'cancel') await page.locator('#cancelProviderCheck').click();
    if (action === 'catalog') {
      await page.getByText('Models and connection', { exact: true }).click();
      await page.locator('#reloadCatalog').click();
    }
    release(); await responseDone;
    await expect(page.locator('#cancelProviderCheck')).toBeHidden();
    await expect(page.locator('#providerKeyStatus')).not.toContainText('Connection verified');
    if (action === 'cancel' || action === 'model') await expect(page.locator('#testProviderKey')).toBeEnabled();
  });
}
