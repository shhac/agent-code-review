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
    const mine = page.locator('.ticket').filter({ hasText: '@octocat' });
    const theirs = page.locator('.ticket').filter({ hasText: '@someone-else' });
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
    await page.locator('.ticket-main').first().click();
    const body = page.locator('.steering-body').first();
    // The engine gets the message verbatim, so the dashboard has to show what
    // that will look like rather than the asterisks.
    await expect(body.locator('strong')).toHaveText('rollback');
    await expect(body.locator('ul li')).toHaveCount(2);
    await expect(body.locator('code')).toHaveText('down');
    await expect(body).not.toContainText('**rollback**');
  });

  test('the editor previews markdown before saving', async ({ page }) => {
    await page.goto('/');
    await page.locator('.ticket-main').first().click();
    await page.locator('.steering button', { hasText: /edit/i }).first().click();

    const modal = page.locator('.modal');
    await expect(modal).toBeVisible();
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
