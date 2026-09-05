import { expect, test } from '@playwright/test';

// The history search runs on the server, over the whole table.
//
// It used to run in the browser over a fixed window of the most recent rows,
// which made the filter box quietly mean "search the last few days": on a real
// 5000-row history, a handle with 21 reviews returned the 3 that happened to
// fall inside the window and looked like the complete answer. The fixture
// seeds 600 rows above the target author, which is more than the 500 the page
// used to fetch, so a search that only looks at loaded rows finds nothing.

const rows = '.review-row';
const search = 'input.filter';
const count = '.matches';

test.describe('history search', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/history');
    await expect(page.locator(rows).first()).toBeVisible();
  });

  test('finds rows that are not on the loaded page', async ({ page }) => {
    // Not on page 1 to begin with: the premise of the test, asserted rather
    // than assumed, so the fixture drifting turns into a failure here instead
    // of a pass that proves nothing.
    await expect(page.getByText('deepsearch-hank').first()).toHaveCount(0);

    await page.locator(search).fill('deepsearch-hank');
    // Rows first: on the build this guards, the search returned nothing, and a
    // failure that says "0 rows" names that. Asserting the label first fails on
    // the label being absent, which is true but not the point.
    await expect(page.locator(rows)).toHaveCount(3);
    await expect(page.locator(count)).toContainText('3 matches');
  });

  test('reports the whole match, not the page', async ({ page }) => {
    await page.locator(search).fill('busy-bot');
    await expect(page.locator(rows)).toHaveCount(25);
    // 600 seeded rows, 25 to a page: the count can only come from the table.
    await expect(page.locator(count)).toContainText('600 matches');
  });

  test('pages without repeating a row', async ({ page }) => {
    await page.locator(search).fill('busy-bot');
    await expect(page.locator(count)).toContainText('600 matches');

    const titleOf = () => page.locator(`${rows} .pr-cell`).allInnerTexts();
    const first = await titleOf();
    await page.getByRole('button', { name: '›' }).click();
    await expect(page.locator(count)).toContainText('600 matches');
    const second = await titleOf();

    expect(second.length).toBe(25);
    expect(first.filter((t) => second.includes(t)), 'pages must not overlap').toEqual([]);
  });

  test('an empty search shows the whole history again', async ({ page }) => {
    await page.locator(search).fill('deepsearch-hank');
    await expect(page.locator(rows)).toHaveCount(3);
    await page.locator(search).fill('');
    await expect(page.locator(count)).toContainText('605 reviews');
    await expect(page.locator(rows)).toHaveCount(25);
  });
});
