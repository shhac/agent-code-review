import { expect, test } from '@playwright/test';

// What a finished review was told, and running it again without leaving the
// page. Steering lives on the queue row and dies with it, so history can only
// answer the first question because completion keeps a copy.
test.describe('history accordion', () => {
  test('shows the instruction a review was given', async ({ page }) => {
    await page.goto('/history');
    const row = page.locator('.review-row-wrap', { hasText: 'cache the tariff lookup' });
    await row.locator('.review-row').click();

    const steering = row.locator('.steering-past');
    await expect(steering).toContainText('weigh the');
    await expect(steering).toContainText('set by @octocat');
    // Markdown, because the engine received it verbatim: showing the asterisks
    // would not be showing what it read.
    await expect(steering.locator('strong')).toHaveText('stale read');
  });

  test('says nothing about steering for a review that records none', async ({ page }) => {
    // The rule an absent value has to obey. Rows written before history kept a
    // copy read absent whatever they were told, so absence may never be
    // rendered as "this review was not steered".
    await page.goto('/history');
    const row = page.locator('.review-row-wrap', { hasText: 'generate entry QR codes' });
    await row.locator('.review-row').click();
    await expect(row.locator('.steering-past')).toHaveCount(0);
    await expect(row).not.toContainText('not steered');
    await expect(row).not.toContainText('No steering');
  });

  test('offers to run the review again from the row', async ({ page }) => {
    await page.goto('/history');
    const row = page.locator('.review-row-wrap', { hasText: 'cache the tariff lookup' });
    await row.locator('.review-row').click();
    await expect(row.getByRole('button', { name: 'Review again' })).toBeEnabled();
  });
});
