import { test, expect, openClient, loadKey } from './fixtures.mjs';

test('model options support arrow keys boundaries selection and Escape focus return', async ({ page }) => {
  await openClient(page); await loadKey(page);
  await page.locator('#modelTrigger').click();
  await page.locator('#modelChoiceToggle').click();
  const choices = page.locator('.model-choice');
  await expect(choices.first()).toBeFocused();
  await page.keyboard.press('End'); await expect(choices.last()).toBeFocused();
  await page.keyboard.press('ArrowDown'); await expect(choices.first()).toBeFocused();
  await page.keyboard.press('ArrowUp'); await expect(choices.last()).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(page.locator('#modelTriggerValue')).toHaveText('test-groq-large');
  await expect(page.locator('#modelTrigger')).toBeFocused();
  await page.locator('#modelTrigger').click();
  await page.keyboard.press('Escape');
  await expect(page.locator('#modelMenu')).toBeHidden();
  await expect(page.locator('#modelTrigger')).toBeFocused();
});

test('input composition and Shift Enter do not send a prompt', async ({ page }) => {
  let requests = 0;
  page.on('request', request => { if (request.url().endsWith('/api/chat')) requests++; });
  await openClient(page); await loadKey(page);
  await page.locator('#input').fill('synthetic composition');
  await page.locator('#input').dispatchEvent('keydown', { key: 'Enter', isComposing: true });
  await page.locator('#input').press('Shift+Enter');
  await expect(page.locator('.turn')).toHaveCount(0);
  expect(requests).toBe(0);
  await page.locator('#input').press('Enter');
  await expect(page.locator('.turn.reply')).toContainText('A blue paper boat.');
  expect(requests).toBe(1);
});

test('keyboard mode navigation skips unavailable Cowork and Settings returns focus', async ({ page }) => {
  await openClient(page);
  await page.locator('#chatTab').focus();
  await page.keyboard.press('ArrowRight');
  await expect(page.locator('#codeTab')).toHaveAttribute('aria-selected', 'true');
  await page.keyboard.press('ArrowRight');
  await expect(page.locator('#chatTab')).toHaveAttribute('aria-selected', 'true');
  await page.locator('#settingsBtn').click();
  await page.keyboard.press('Escape');
  await expect(page.locator('#settingsDialog')).not.toBeVisible();
  await expect(page.locator('#settingsBtn')).toBeFocused();
});
