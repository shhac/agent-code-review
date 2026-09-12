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

test.describe('score tiers', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/config');
    await page.getByRole('tab', { name: 'Settings' }).click();
    await expect(page.locator('.tier-table tbody tr').first()).toBeVisible();
  });

  test('states every tier the chart draws', async ({ page }) => {
    const names = await page.locator('.tier-table tbody tr td:first-child').allTextContents();
    const bands = await page.locator('.curve .band').allTextContents();
    expect(names).toEqual(bands.map((b) => b.trim()));
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

  test('prices a PR the way the scorer does', async ({ page }) => {
    // 200 added and 100 removed is 350 churn once a removal weighs 1.5, which
    // is inside "large" and paid at 0.88x on the way down. The figures come
    // from the daemon, so this pins that the page is showing its answer and
    // not an estimate of its own.
    await expect(page.locator('.calc-total')).toContainText('+110');
    await expect(page.locator('.calc-why')).toContainText('350');
    await expect(page.locator('.calc-why')).toContainText('large');
    // Churn is the one term this tab leans on everywhere and defines nowhere,
    // so the working is shown rather than just the total.
    await expect(page.locator('.calc-why')).toContainText('1.5x');
  });

  test('turns negative when the verdict costs points', async ({ page }) => {
    await page.locator('.calc select').last().selectOption('REQUESTED_CHANGES');
    await expect(page.locator('.calc-total')).toContainText('-27');
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
    expect(scores.map(Number)).toEqual([190, 158, 135]);
    await expect(page.locator('.probe-state')).toHaveText('tighter wins');
  });

  test('re-surveys when a dial moves, and says it is a draft', async ({ page }) => {
    // The exponent back at 1 is the old proportional policy, where a bigger
    // PR always earns more: the ordering must flip.
    await page.locator('#dial-churn_exponent').fill('1');
    await page.locator('#dial-churn_exponent').dispatchEvent('input');
    await expect(page.locator('.probe-state')).toHaveText('bigger wins');
    await expect(page.locator('.shape-state')).toContainText('churn_exponent');

    await page.getByRole('button', { name: 'reset' }).click();
    await expect(page.locator('.probe-state')).toHaveText('tighter wins');
  });

  test('hands back a config block for the policy on screen', async ({ page }) => {
    const json = JSON.parse(await page.locator('.policy-json').textContent() ?? '{}');
    expect(Object.keys(json)).toEqual(['scoring']);
    expect(json.scoring.churn_exponent).toBe(0.15);
    // The open-ended tier omits max_churn, which is what the format means by
    // open-ended; a 0 would read as a tier covering nothing.
    expect(json.scoring.buckets.at(-1)).not.toHaveProperty('max_churn');
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

// Where a tier sits is decided by its bound, so the table orders itself and
// nobody has to drag anything.
test('a new tier lands where its bound puts it', async ({ page }) => {
  await page.goto('/config');
  await page.getByRole('tab', { name: 'Score tuning' }).click();
  await expect(page.locator('.tier-edit tbody tr')).toHaveCount(5);

  await page.getByRole('button', { name: '+ tier' }).click();
  await expect(page.locator('.tier-edit tbody tr')).toHaveCount(6);

  // 120 belongs between small (50) and medium (250), not at the end where it
  // was added.
  const added = page.locator('.tier-edit tbody tr').nth(4).locator('input[type=number]').first();
  await added.fill('120');
  await added.blur();

  const order = await page.locator('.tier-edit tbody tr').evaluateAll((rows) =>
    rows.map((r) => (r.querySelectorAll('input')[1] as HTMLInputElement).value),
  );
  expect(order).toEqual(['10', '50', '120', '250', '1000', 'open']);
});
