import { test, expect, openClient, loadKey, sendPrompt, syntheticKey } from './fixtures.mjs';

async function records(page) {
  return page.evaluate(() => new Promise((resolve, reject) => {
    const request = indexedDB.open('osanwe-conversations', 1);
    request.onupgradeneeded = () => request.result.createObjectStore('conversations', { keyPath: 'id' });
    request.onerror = () => reject(new Error('Test database could not open'));
    request.onsuccess = () => {
      const db = request.result, tx = db.transaction('conversations');
      const read = tx.objectStore('conversations').getAll();
      tx.oncomplete = () => { db.close(); resolve(read.result); };
    };
  }));
}

test('ephemeral chats are not written to device history', async ({ page }) => {
  await openClient(page); await loadKey(page); await sendPrompt(page);
  await expect(page.locator('#stop')).toBeHidden();
  expect(await records(page)).toEqual([]);
});

test('opt-in history stores conversation text without the provider key and can be deleted', async ({ page }) => {
  await openClient(page); await loadKey(page); await sendPrompt(page);
  await expect(page.locator('#stop')).toBeHidden();
  await page.locator('#settingsBtn').click();
  await page.locator('input[name="retention"][value="device"]').check();
  await expect(page.locator('#settingsStatus')).toContainText('saved only');
  const saved = await records(page);
  expect(saved).toHaveLength(1);
  expect(saved[0].turns).toHaveLength(2);
  expect(JSON.stringify(saved)).not.toContain(syntheticKey);
  page.once('dialog', dialog => dialog.accept());
  await page.locator('#deleteHistoryBtn').click();
  await expect(page.locator('#settingsStatus')).toContainText('were deleted');
  expect(await records(page)).toEqual([]);
});
