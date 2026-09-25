import { test, expect, openClient, syntheticKey } from './fixtures.mjs';

test('a new visitor must load a key and acknowledge the hosted privacy boundary', async ({ page }) => {
  await openClient(page);
  await expect(page.locator('#input')).toBeDisabled();
  await expect(page.locator('#send')).toBeDisabled();
  await page.locator('#openProviderSettings').click();
  await expect(page.locator('.provider-warning')).toContainText('forwards the key, prompts, and answers');
  await page.locator('#providerKey').fill(syntheticKey);
  await page.locator('#connectProviderKey').click();
  await expect(page.locator('#providerKeyStatus')).toContainText('Confirm');
  await expect(page.locator('#providerConsent')).toBeFocused();
  await expect(page.locator('#testProviderKey')).toBeHidden();
  await page.locator('#providerConsent').check();
  await page.locator('#connectProviderKey').click();
  await expect(page.locator('#providerKey')).toHaveValue('');
  await expect(page.locator('#providerKey')).toBeDisabled();
});

test('an invalid key is rejected locally without a connection request', async ({ page }) => {
  let calls = 0;
  page.on('request', request => { if (request.url().endsWith('/api/providers/check')) calls++; });
  await openClient(page);
  await page.locator('#settingsBtn').click();
  await page.locator('#providerConsent').check();
  await page.locator('#providerKey').fill('invalid key with spaces');
  await page.locator('#connectProviderKey').click();
  await expect(page.locator('#providerKeyStatus')).toContainText('without spaces');
  expect(calls).toBe(0);
});
