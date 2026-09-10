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

  test('puts the review-again action at the far edge, not beside the log link', async ({ page }) => {
    // Geometry, because "right-aligned" is not visible in the markup: the
    // button and the log link are siblings in one flex row, and only the
    // computed layout says which end each landed on.
    await page.goto('/history');
    const row = page.locator('.review-row-wrap', { hasText: 'cache the tariff lookup' });
    await row.locator('.review-row').click();

    const actions = row.locator('.detail-actions');
    const box = await actions.boundingBox();
    const button = await row.getByRole('button', { name: 'Review again' }).boundingBox();
    if (!box || !button) throw new Error('actions row or button not laid out');

    // Its right edge tracks the container's, whatever the row is wide.
    expect(Math.abs(box.x + box.width - (button.x + button.width))).toBeLessThan(2);
    // And it is genuinely at the far end rather than merely last in a huddle.
    expect(button.x).toBeGreaterThan(box.x + box.width / 2);
  });

  test('leaves a gutter between the content and the highlighted box', async ({ page }) => {
    // An open row paints a background across its full width. Every child —
    // the PR cell, the detail list, the actions — sat flush against that
    // edge, so text and button touched the box they were in.
    await page.goto('/history');
    const wrap = page.locator('.review-row-wrap', { hasText: 'cache the tariff lookup' });
    await wrap.locator('.review-row').click();

    const outer = await wrap.boundingBox();
    if (!outer) throw new Error('row not laid out');
    const inset = async (sel: string) => {
      const b = await wrap.locator(sel).first().boundingBox();
      if (!b) throw new Error(`${sel} not laid out`);
      return { left: b.x - outer.x, right: outer.x + outer.width - (b.x + b.width) };
    };

    for (const sel of ['.pr-cell', '.detail-actions']) {
      const { left, right } = await inset(sel);
      expect(left, `${sel} left gutter`).toBeGreaterThanOrEqual(8);
      expect(right, `${sel} right gutter`).toBeGreaterThanOrEqual(8);
    }
  });

  test('offers to run the review again from the row', async ({ page }) => {
    await page.goto('/history');
    const row = page.locator('.review-row-wrap', { hasText: 'cache the tariff lookup' });
    await row.locator('.review-row').click();
    await expect(row.getByRole('button', { name: 'Review again' })).toBeEnabled();
  });
});
