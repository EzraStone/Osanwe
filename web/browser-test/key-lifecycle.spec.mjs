import { test, expect, openClient, loadKey, syntheticKey } from './fixtures.mjs';

test('keys are absent from browser storage and are cleared by a reload', async ({ page }) => {
  await openClient(page); await loadKey(page);
  const stored = await page.evaluate(() => JSON.stringify({ local: { ...localStorage }, session: { ...sessionStorage } }));
  expect(stored).not.toContain(syntheticKey);
  await page.reload();
  await expect(page.locator('#input')).toBeDisabled();
  await page.locator('#settingsBtn').click();
  await expect(page.locator('#providerKey')).toHaveValue('');
  await expect(page.locator('#connectProviderKey')).toBeVisible();
});

test('forgetting a key restores an editable empty field and locks chat', async ({ page }) => {
  await openClient(page); await loadKey(page, { close: false });
  await page.locator('#forgetProviderKey').click();
  await expect(page.locator('#providerKey')).toBeEditable();
  await expect(page.locator('#providerKey')).toHaveValue('');
  await expect(page.locator('#testProviderKey')).toBeHidden();
  await page.locator('#closeSettingsIcon').click();
  await expect(page.locator('#input')).toBeDisabled();
});

test('restored pages cannot retain usable credentials after pagehide', async ({ page }) => {
  await openClient(page); await loadKey(page);
  await page.evaluate(() => {
    window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true }));
    window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }));
  });
  await expect(page.locator('#input')).toBeDisabled();
  await page.locator('#settingsBtn').click();
  await expect(page.locator('#providerKey')).toBeEditable();
});
