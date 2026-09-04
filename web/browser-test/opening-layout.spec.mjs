import { expect, test } from '@playwright/test';

const providerCatalog = {
  providers: [{ id: 'tokenrouter', label: 'TokenRouter', models: ['z-ai/glm-5.3-free'] }],
};

for (const viewport of [
  { name: 'compact desktop', width: 771, height: 436 },
  { name: 'phone', width: 390, height: 844 },
]) {
  test(`opening hierarchy stays readable on ${viewport.name}`, async ({ page }) => {
    await page.setViewportSize({ width: viewport.width, height: viewport.height });
    await page.route('**/api/providers', (route) => route.fulfill({ json: providerCatalog }));
    await page.goto('/client');

    const opening = page.locator('#opening');
    const action = page.locator('#openProviderSettings');
    const boundary = page.locator('.hosted-boundary');
    await expect(page.locator('#openingTitle')).toHaveText('What are you thinking about?');
    await expect(page.locator('#openingSub')).toHaveText('Choose a provider and model to begin.');
    await expect(action).toHaveText('Connect a provider');
    await expect(action).toBeVisible();
    await expect(boundary).toContainText('This host processes your key, prompt, and answer in transit.');
    await expect(page.locator('#modeKicker')).toHaveCount(0);

    const layout = await opening.evaluate((element) => {
      const actionElement = element.querySelector('#openProviderSettings');
      const boundaryElement = element.querySelector('.hosted-boundary');
      const actionBox = actionElement.getBoundingClientRect();
      const boundaryBox = boundaryElement.getBoundingClientRect();
      const boundaryStyle = getComputedStyle(boundaryElement);
      const markStyle = getComputedStyle(element, '::before');
      return {
        actionHeight: actionBox.height,
        boundaryAfterAction: boundaryBox.top > actionBox.bottom,
        boundaryBackground: boundaryStyle.backgroundColor,
        boundaryBorderTop: boundaryStyle.borderTopWidth,
        boundaryBorderLeft: boundaryStyle.borderLeftWidth,
        markOpacity: Number.parseFloat(markStyle.opacity),
        noHorizontalOverflow: document.documentElement.scrollWidth <= window.innerWidth,
      };
    });

    expect(layout.actionHeight).toBeGreaterThanOrEqual(42);
    expect(layout.boundaryAfterAction).toBe(true);
    expect(layout.boundaryBackground).toBe('rgba(0, 0, 0, 0)');
    expect(layout.boundaryBorderTop).toBe('1px');
    expect(layout.boundaryBorderLeft).toBe('0px');
    expect(layout.markOpacity).toBeLessThanOrEqual(0.05);
    expect(layout.noHorizontalOverflow).toBe(true);
  });
}
