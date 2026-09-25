import { test, expect, catalog } from './fixtures.mjs';

for (const failure of ['http', 'malformed']) {
  test(`startup recovers from a ${failure} catalog without inference or page reload`, async ({ page }) => {
    let requests = 0, inference = 0;
    page.on('request', request => { if (request.method() === 'POST') inference++; });
    await page.route('**/api/providers', route => {
      requests++;
      if (requests > 1) return route.fulfill({ json: catalog });
      return failure === 'http' ? route.fulfill({ status: 503, body: 'temporarily unavailable' }) : route.fulfill({ json: { providers: [null] } });
    });
    await page.goto('/client');
    await expect(page.locator('#startupError')).toBeVisible();
    await expect(page.locator('#input')).toBeDisabled();
    await page.locator('#retryStartup').click();
    await expect(page.locator('#startupError')).toBeHidden();
    await expect(page.locator('#openProviderSettings')).toBeVisible();
    await expect(page.locator('#providerSelect option')).toHaveCount(2);
    expect(inference).toBe(0);
    expect(requests).toBe(2);
  });
}
