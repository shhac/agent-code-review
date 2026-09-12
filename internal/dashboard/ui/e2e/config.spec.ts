import { expect, test } from '@playwright/test';

// The Config page's two scoring panels. Both exist to explain a number, so
// what is worth asserting is that they agree with the daemon and with each
// other rather than that they render.
test.describe('the tabs split the page by who is asking', () => {
  test('opens on the roster and keeps the other two out of the way', async ({ page }) => {
    await page.goto('/config');
    await expect(page.getByRole('tab', { name: 'Repos & authors' })).toHaveAttribute('aria-selected', 'true');
    await expect(page.locator('.authors')).toBeVisible();
    await expect(page.locator('.settings')).toHaveCount(0);
    await expect(page.locator('.shape')).toHaveCount(0);
  });

  test('shows what the reviewer is set to do, and then the tools for it', async ({ page }) => {
    await page.goto('/config');
    await page.getByRole('tab', { name: 'Settings' }).click();
    await expect(page.locator('.settings')).toBeVisible();
    await expect(page.locator('.tier-table')).toBeVisible();
    await expect(page.locator('.authors')).toHaveCount(0);

    await page.getByRole('tab', { name: 'Score tuning' }).click();
    await expect(page.locator('.calc-total')).toBeVisible();
    await expect(page.locator('.shape canvas').first()).toBeVisible();
    await expect(page.locator('.settings')).toHaveCount(0);
  });
});

test.describe('score shape', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/config');
    await page.getByRole('tab', { name: 'Settings' }).click();
    await expect(page.locator('.tier-table tbody tr').first()).toBeVisible();
  });

  // The two sizes the whole policy is built around. They are DERIVED from the
  // dials, so the page must not be working them out for itself.
  test('states the two sizes the policy is built around', async ({ page }) => {
    const summary = page.locator('.shape-summary');
    await expect(summary).toContainText('50 changed lines');
    await expect(summary).toContainText('200 changed lines');
    await expect(summary).toContainText('100 points');
  });

  test('quotes the open-ended tier without an upper bound', async ({ page }) => {
    const last = page.locator('.tier-table tbody tr').last();
    await expect(last.locator('td').nth(1)).toContainText('+');
  });
});

test.describe('score calculator', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/config');
    await page.getByRole('tab', { name: 'Score tuning' }).click();
    await expect(page.locator('.calc-total')).toBeVisible();
  });

  test('prices a PR the way the scorer does, and shows both halves', async ({ page }) => {
    // 100 added and 200 removed is 300 changed lines, past the 200-line peak,
    // plus 100 net removed. The figures come from the daemon, so this pins
    // that the page is showing its answer rather than an estimate of its own,
    // and that a total which does not decompose is never shown alone.
    await expect(page.locator('.calc-total')).toContainText('+115');
    await expect(page.locator('.calc-why')).toContainText('300');
    await expect(page.locator('.calc-why')).toContainText('large');
    await expect(page.locator('.calc-why')).toContainText('100 net lines removed');
  });

  test('turns negative when the verdict costs points', async ({ page }) => {
    await page.locator('.calc select').last().selectOption('REQUESTED_CHANGES');
    await expect(page.locator('.calc-total')).toContainText('-24');
    await expect(page.locator('.calc-total.bad')).toBeVisible();
  });

  test('shows the decay once there is more than one round', async ({ page }) => {
    // The rounds list is the whole reason the calculator models a sequence:
    // approving at round three is worth less than approving at round one, and
    // a single-figure answer would hide it.
    await expect(page.locator('.calc-rounds li')).toHaveCount(0);
    await page.locator('.calc select').first().selectOption('3');
    await expect(page.locator('.calc-rounds li')).toHaveCount(3);

    const scores = await page.locator('.calc-rounds li b').allTextContents();
    const finalRound = Number(scores[2].replace('+', ''));
    expect(finalRound).toBeLessThan(110);
    expect(finalRound).toBeGreaterThan(0);
  });
});

// The tuning panel surveys a whole candidate policy. Every figure on it is
// the daemon's answer, so what is worth asserting is that moving a dial
// changes the answer and that the panel says whose policy it is showing.
test.describe('score shape', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/config');
    await page.getByRole('tab', { name: 'Score tuning' }).click();
    await expect(page.locator('.shape .probe b').first()).toBeVisible();
  });

  test('opens on the running policy and answers the tighter-solve question', async ({ page }) => {
    await expect(page.locator('.shape-state')).toContainText('running');
    const scores = await page.locator('.shape .probe b').allTextContents();
    expect(scores.map(Number)).toEqual([100, 86, 71]);
    await expect(page.locator('.probe-state')).toHaveText('tighter wins');
  });

  test('re-surveys when a dial moves, and says it is a draft', async ({ page }) => {
    // A piece size of 400 lines moves the peak out past every probe, so the
    // biggest of the three becomes the best-paid: the ordering must flip.
    await page.locator('#dial-piece_lines').fill('400');
    await page.locator('#dial-piece_lines').dispatchEvent('input');
    await expect(page.locator('.probe-state')).toHaveText('bigger wins');
    await expect(page.locator('.shape-state')).toContainText('piece_lines');

    await page.getByRole('button', { name: 'reset' }).click();
    await expect(page.locator('.probe-state')).toHaveText('tighter wins');
  });

  test('hands back a config block for the policy on screen', async ({ page }) => {
    const json = JSON.parse(await page.locator('.policy-json').textContent() ?? '{}');
    expect(Object.keys(json)).toEqual(['scoring']);
    expect(json.scoring).toEqual({
      piece_lines: 50, size_points: 100, size_falloff: 3, removal_points_per_100: 20,
    });
  });
});

// The tab strip is a label on a line, and the base button rule in app.css is
// a filled pill. Inheriting it curled the active tab's underline into a smile
// and lifted the tab off the rule on hover; both are geometry, so both are
// checked as geometry.
test.describe('the tab strip', () => {
  test.beforeEach(async ({ page }) => await page.goto('/config'));

  test('underlines the active tab with a straight rule', async ({ page }) => {
    const active = page.getByRole('tab', { selected: true });
    await expect(active).toHaveCSS('border-bottom-left-radius', '0px');
    await expect(active).toHaveCSS('border-bottom-right-radius', '0px');
  });

  test('lines its rule up with the one above it', async ({ page }) => {
    const head = await page.locator('.page-head').boundingBox();
    const tabs = await page.locator('.page-tabs').boundingBox();
    if (!head || !tabs) throw new Error('not laid out');
    // Two horizontal rules 40px apart with different lengths read as a
    // mistake. They share a left edge already; this is the right one.
    expect(Math.abs(head.x + head.width - (tabs.x + tabs.width))).toBeLessThan(2);
  });
});


