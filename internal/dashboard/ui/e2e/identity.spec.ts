import { expect, test } from '@playwright/test';
import { DAEMON_PORT } from './fixture.mjs';

// Identity and steering, driven through a proxy that attaches the header the
// way `tailscale serve` does. The daemon's own tests cover the rules; these
// cover that the assembled page reflects them, which is where a viewer finds
// out whether they were recognised.

test.describe('through the tailscale proxy', () => {
  test('the chip names the viewer', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('.viewer-chip')).toContainText('@octocat');
    await expect(page.locator('.viewer-chip')).toContainText('can steer your own PRs');
  });

  test('an author may steer their own PR but not another', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('.ticket-main').first()).toBeVisible();
    for (const t of await page.locator('.ticket-main').all()) await t.click();

    // Anchor each ticket by its AUTHOR, never by the refusal wording: filtering
    // on the refusal makes the negative assertion vacuous, because broken
    // authorisation removes the refusal and the filter then matches nothing.
    // The queued one specifically: octocat also owns a row under review, whose
    // editor is closed for a reason that has nothing to do with authorship.
    const mine = page.locator('.ticket').filter({ hasText: 'Add retry to the HTTP client' });
    const theirs = page.locator('.ticket').filter({ hasText: '@someone-else' });
    await expect(mine).toContainText('@octocat');
    await expect(mine).toHaveCount(1);
    await expect(theirs).toHaveCount(1);

    await expect(mine.locator('.steering button', { hasText: /edit/i })).toHaveCount(1);
    // The control is absent AND the refusal is shown: one without the other is
    // a half-broken state that should still fail.
    await expect(theirs.locator('.steering button')).toHaveCount(0);
    await expect(theirs.locator('.steering')).toContainText('Only @someone-else');
  });

  test('steering renders as markdown, not as raw syntax', async ({ page }) => {
    await page.goto('/');
    // By title, not by position: a reviewing row is pinned to the top of the
    // board, so "first" is not the row this test is about.
    const ticket = page.locator('.ticket', { hasText: 'Add retry to the HTTP client' });
    await ticket.locator('.ticket-main').click();
    const body = ticket.locator('.steering-body');
    // The engine gets the message verbatim, so the dashboard has to show what
    // that will look like rather than the asterisks.
    await expect(body.locator('strong')).toHaveText('rollback');
    await expect(body.locator('ul li')).toHaveCount(2);
    await expect(body.locator('code')).toHaveText('down');
    await expect(body).not.toContainText('**rollback**');
  });

  test('the editor previews markdown before saving', async ({ page }) => {
    await page.goto('/');
    const ticket = page.locator('.ticket', { hasText: 'Add retry to the HTTP client' });
    await ticket.locator('.ticket-main').click();
    await ticket.locator('.steering button', { hasText: /edit/i }).click();

    const modal = page.locator('.modal');
    await expect(modal).toBeVisible();
    // Opening the editor parks the PR, and the footer says so when it could
    // not. One assertion, but it drives QueuedPR, the authorisation ladder and
    // the hold write through real SQL, none of which the Go fakes exercise.
    await expect(modal).not.toContainText('not held');
    await modal.locator('textarea').fill('## Heading\n\n- one\n- two');
    await modal.locator('.pill-toggle button', { hasText: /preview/i }).click();
    await expect(modal.locator('.steer-preview h2')).toHaveText('Heading');
    await expect(modal.locator('.steer-preview ul li')).toHaveCount(2);
  });
});

test.describe('the proxy is the source of identity', () => {
  // A client-supplied header must not survive the proxy: Tailscale strips any
  // incoming copy before adding its own, and that stripping is the only reason
  // the value can be trusted at all.
  test('a forged header is replaced, not honoured', async ({ page }) => {
    await page.setExtraHTTPHeaders({ 'Tailscale-User-Login': 'paul@example.com' });
    await page.goto('/');
    // paul@example.com maps to @paul-gh, the operator, who may steer anything.
    // Seeing @octocat proves the forgery was discarded.
    await expect(page.locator('.viewer-chip')).toContainText('@octocat');
    await expect(page.locator('.viewer-chip')).not.toContainText('paul-gh');
    await expect(page.locator('.viewer-chip')).not.toContainText('can steer any PR');
  });
});

// A request that never passes through the proxy carries no identity Tailscale
// vouches for. That is asserted in Go (TestSteeringRejectsForgedIdentity),
// where the peer address can be set directly: this harness connects from
// localhost, which IS a loopback peer, so it cannot express a tailnet or
// public client. The daemon binds loopback precisely so those cannot reach it.

// A review in flight fixed its instructions when it was dispatched and never
// re-reads the row, so the editor must not offer an edit that would reach
// nothing. The server refuses the write too; this is that same rule, shown.
test.describe('a PR under review', () => {
  test('offers no way to edit the steering that is already running', async ({ page }) => {
    await page.goto('/');
    const ticket = page.locator('.ticket', { hasText: 'Rework the cache layer' });
    await ticket.locator('.ticket-main').click();

    const steering = ticket.locator('.steering');
    await expect(steering).toContainText('Check the eviction policy');
    await expect(steering.getByRole('button', { name: 'edit' })).toHaveCount(0);
    await expect(steering).toContainText('shaping the review running now');
  });

  test('a queued PR still offers the editor', async ({ page }) => {
    await page.goto('/');
    const ticket = page.locator('.ticket', { hasText: 'Add retry to the HTTP client' });
    await ticket.locator('.ticket-main').click();
    await expect(ticket.locator('.steering').getByRole('button', { name: 'edit' })).toBeVisible();
  });
});

// Enter is the newline key in both steering flows, and both draft into a
// textarea inside a modal whose backdrop closes on Enter. keydown bubbles, so
// the dismiss handler used to answer for keystrokes aimed at the textarea and
// discard the draft. Driven through the real browser because bubbling is the
// whole point: nothing short of a real event reproduces it.
test.describe('typing in a modal', () => {
  test('Enter writes a newline instead of discarding the draft', async ({ page }) => {
    await page.goto('/');
    const ticket = page.locator('.ticket', { hasText: 'Add retry to the HTTP client' });
    await ticket.locator('.ticket-main').click();
    await ticket.locator('.steering button', { hasText: /edit/i }).click();

    const modal = page.locator('.modal');
    const box = modal.locator('textarea');
    await box.fill('first line');
    await box.press('Enter');
    await box.type('second line');

    await expect(modal).toBeVisible();
    await expect(box).toHaveValue('first line\nsecond line');
  });

  test('Escape still closes it', async ({ page }) => {
    await page.goto('/');
    const ticket = page.locator('.ticket', { hasText: 'Add retry to the HTTP client' });
    await ticket.locator('.ticket-main').click();
    await ticket.locator('.steering button', { hasText: /edit/i }).click();

    const modal = page.locator('.modal');
    await expect(modal).toBeVisible();
    await modal.locator('textarea').press('Escape');
    await expect(modal).toBeHidden();
  });
});
