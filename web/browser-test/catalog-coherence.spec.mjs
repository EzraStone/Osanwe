import { test, expect, openClient, loadKey, catalog } from './fixtures.mjs';

async function refreshSuggestions(page) {
  await page.getByText('Models and connection', { exact: true }).click();
  await page.locator('#reloadCatalog').click();
}

test('Settings catalog refresh can recover a failed initial provider load', async ({ page }) => {
  let requests = 0;
  await page.route('**/api/providers', route => {
    requests++;
    return requests === 1 ? route.fulfill({ status: 503, body: 'Unavailable' }) : route.fulfill({ json: catalog });
  });
  await page.goto('/client');
  await expect(page.locator('#startupError')).toBeVisible();
  await page.locator('#settingsBtn').click();
  await refreshSuggestions(page);
  await expect(page.locator('#providerSelect option')).toHaveCount(2);
  await expect(page.locator('#providerModelSuggestions option')).toHaveCount(2);
  await page.locator('#closeSettingsIcon').click();
  await expect(page.locator('#startupError')).toBeHidden();
  await expect(page.locator('#openProviderSettings')).toBeVisible();
  expect(requests).toBe(2);
});

test('catalog refresh updates provider labels, new providers and model suggestions together', async ({ page }) => {
  await openClient(page); await loadKey(page, { close: false });
  const updated = { providers: [
    { id: 'groq', label: 'Updated Groq', models: ['updated-model'] },
    ...catalog.providers.slice(1),
    { id: 'test-provider', label: 'Test Provider', models: ['other'] },
  ] };
  await page.route('**/api/providers', route => route.fulfill({ json: updated }));
  await refreshSuggestions(page);
  await expect(page.locator('#providerSelect option')).toHaveCount(3);
  await expect(page.locator('#providerSelect option:checked')).toHaveText('Updated Groq');
  await expect(page.locator('#providerModelSuggestions option')).toHaveCount(1);
  await expect(page.locator('#providerModelSuggestions option')).toHaveAttribute('value', 'updated-model');
});

test('removing the selected provider forgets its key before a replacement can be used', async ({ page }) => {
  await openClient(page); await loadKey(page, { close: false });
  await page.route('**/api/providers', route => route.fulfill({ json: { providers: catalog.providers.slice(1) } }));
  await refreshSuggestions(page);
  await expect(page.locator('#providerSelect')).toHaveValue('tokenrouter');
  await expect(page.locator('#providerKey')).toBeEditable();
  await expect(page.locator('#providerKey')).toHaveValue('');
  await expect(page.locator('#testProviderKey')).toBeHidden();
  await expect(page.locator('#providerKeyStatus')).toContainText('no longer listed');
  await page.locator('#closeSettingsIcon').click();
  await expect(page.locator('#input')).toBeDisabled();
});
