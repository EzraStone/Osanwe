import { test, expect, openClient, loadKey, sendPrompt } from './fixtures.mjs';

test('provider changes require reloading credentials and replace model suggestions', async ({ page }) => {
  await openClient(page); await loadKey(page, { close: false });
  await page.locator('#providerSelect').selectOption('tokenrouter');
  await expect(page.locator('#providerKeyStatus')).toContainText('Provider changed to TokenRouter');
  await expect(page.locator('#providerKey')).toBeEditable();
  await expect(page.locator('#providerModel')).toHaveValue('z-ai/glm-5.3-free');
  await expect(page.locator('#testProviderKey')).toBeHidden();
  await page.locator('#closeSettingsIcon').click();
  await loadKey(page, { provider: 'tokenrouter' });
  const request = page.waitForRequest('**/api/chat');
  await sendPrompt(page);
  expect((await request).postDataJSON()).toMatchObject({ provider: 'tokenrouter', model: 'z-ai/glm-5.3-free' });
});

test('custom model selection is preserved exactly in the outbound request', async ({ page }) => {
  await openClient(page); await loadKey(page, { close: false });
  await page.locator('#providerModel').fill('test/custom-model:version');
  await page.locator('#useProviderModel').click();
  await page.locator('#closeSettingsIcon').click();
  const request = page.waitForRequest('**/api/chat');
  await sendPrompt(page);
  expect((await request).postDataJSON().model).toBe('test/custom-model:version');
});
