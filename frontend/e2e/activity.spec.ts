import { test, expect, type Page } from '@playwright/test';

// Activity log (console + gateway sources, filters, detail, skip link),
// Settings production checklist, and login-page copy.

const baseURL = process.env.PLAYWRIGHT_BASE_URL ?? 'http://127.0.0.1:8088';

async function stackUp(): Promise<boolean> {
  try {
    const res = await fetch(`${baseURL}/api/health/live`, { signal: AbortSignal.timeout(5000) });
    return res.ok;
  } catch {
    return false;
  }
}

test.beforeAll(async () => {
  if (!(await stackUp())) {
    test.skip(true, `No stack running at ${baseURL}; start it with: docker compose up -d --build`);
  }
});

async function login(page: Page): Promise<void> {
  const password = process.env.E2E_ADMIN_PASSWORD;
  expect(password, 'E2E_ADMIN_PASSWORD must be set to run the activity/settings spec').toBeTruthy();
  await page.goto('/');
  await page.getByLabel('Username').fill('admin');
  await page.getByLabel('Password').fill(password!);
  await page.getByRole('button', { name: 'Sign in to console' }).click();
  await expect(page.getByRole('button', { name: 'Buckets & objects' })).toBeVisible();
}

async function openActivity(page: Page): Promise<void> {
  await login(page);
  await page.getByRole('button', { name: 'Activity log' }).first().click();
  await expect(page.getByRole('heading', { name: 'Recent activity' })).toBeVisible();
  await expect(page.locator('.activity-row .activity-title strong').first()).toBeVisible();
}

async function auditTotal(page: Page, query: string): Promise<number> {
  const res = await page.request.get(`/api/audit?${query}`);
  expect(res.ok(), `GET /api/audit?${query} should succeed`).toBeTruthy();
  const body = (await res.json()) as { total?: number; items?: unknown[] };
  return body.total ?? body.items?.length ?? 0;
}

async function gatewayTotal(page: Page): Promise<number> {
  return auditTotal(page, 'source=gateway&limit=1');
}

async function rowTitles(page: Page): Promise<string[]> {
  const titles = await page.locator('.activity-row .activity-title strong').allTextContents();
  return titles.map((t) => t.trim());
}

function filterButton(page: Page, name: string) {
  return page
    .getByRole('group', { name: 'Filter activity' })
    .getByRole('button', { name, exact: true });
}

test.describe('activity log and settings', () => {
  // Breaks if: Source=All stops combining console and gateway rows (the three server-side totals must add up and the list must show the combined total).
  test('activity log shows both sources', async ({ page }) => {
    await openActivity(page);
    const total = async (query: string) => {
      const res = await page.request.get(`/api/audit?limit=1${query}`);
      return ((await res.json()) as { total: number }).total;
    };
    const [all, console_, gateway] = [await total(''), await total('&source=console'), await total('&source=gateway')];
    expect(console_, 'expected at least one console row (the login event)').toBeGreaterThan(0);
    expect(all).toBeGreaterThanOrEqual(console_ + gateway);
    await expect(page.getByText(/Showing 1-\d+ of /).first()).toBeVisible();

    if (gateway === 0) {
      test.info().annotations.push({
        type: 'note',
        description: 'No gateway rows on this stack; gateway half of this test not exercised.',
      });
      return;
    }
    // Newest rows are console events when other specs run in parallel, so look at the gateway rows through the filter.
    await filterButton(page, 'Gateway').click();
    const gatewayRow = page
      .locator('.activity-row')
      .filter({ has: page.locator('.activity-chip') })
      .first();
    await expect(gatewayRow).toBeVisible();
    await expect(gatewayRow.locator('.activity-chip')).toHaveText(/^(allow|deny)$/);
    await expect(gatewayRow.locator('.activity-code')).toHaveText(/^\d{3}$/);
  });

  // Breaks if: the Source filter stops filtering server-side or leaks the other source.
  test('source filter shows only the chosen source', async ({ page }) => {
    await openActivity(page);
    const gateway = await gatewayTotal(page);
    test.skip(gateway === 0, 'No gateway rows on this stack (GET /api/audit?source=gateway&limit=1 returned none); run scripts/gateway_e2e.py first');

    await filterButton(page, 'Gateway').click();
    await expect(page.locator('.activity-row .activity-chip').first()).toBeVisible();
    let titles = await rowTitles(page);
    expect(titles.length).toBeGreaterThan(0);
    expect(titles.every((t) => t.startsWith('S3 '))).toBeTruthy();
    expect(titles).not.toContain('login');

    await filterButton(page, 'Console').click();
    // Other specs run in parallel and add console events (upload, create_bucket), so do not assume the newest one is login.
    await expect(page.locator('.activity-row .activity-title strong').first()).not.toHaveText(/^S3 /);
    titles = await rowTitles(page);
    expect(titles.length).toBeGreaterThan(0);
    expect(titles.every((t) => !t.startsWith('S3 '))).toBeTruthy();
    await expect(page).toHaveURL(/#\/activity/);
  });

  // Breaks if: the Decision filter mixes allowed and denied gateway requests.
  test('decision filter shows only the chosen decision', async ({ page }) => {
    await openActivity(page);
    const denied = await auditTotal(page, 'source=gateway&decision=deny&limit=1');
    const allowed = await auditTotal(page, 'source=gateway&decision=allow&limit=1');
    test.skip(denied === 0, 'No denied gateway rows on this stack; run scripts/gateway_e2e.py first');
    test.skip(allowed === 0, 'No allowed gateway rows on this stack; run scripts/gateway_e2e.py first');

    await filterButton(page, 'Gateway').click();
    await expect(page.locator('.activity-row .activity-chip').first()).toBeVisible();

    await filterButton(page, 'Deny').click();
    await expect(page.locator('.activity-row .activity-chip').first()).toHaveClass(/is-deny/);
    let chips = (await page.locator('.activity-row .activity-chip').allTextContents()).map((t) =>
      t.trim(),
    );
    expect(chips.length).toBeGreaterThan(0);
    expect(chips.every((c) => c === 'deny')).toBeTruthy();
    expect(chips).not.toContain('allow');

    await filterButton(page, 'Allow').click();
    await expect(page.locator('.activity-row .activity-chip').first()).toHaveClass(/is-allow/);
    chips = (await page.locator('.activity-row .activity-chip').allTextContents()).map((t) =>
      t.trim(),
    );
    expect(chips.length).toBeGreaterThan(0);
    expect(chips.every((c) => c === 'allow')).toBeTruthy();
    expect(chips).not.toContain('deny');
  });

  // Breaks if: gateway rows lose their descriptive accessible name for assistive tech.
  test('gateway row has a descriptive accessible name', async ({ page }) => {
    await openActivity(page);
    const gateway = await gatewayTotal(page);
    test.skip(gateway === 0, 'No gateway rows on this stack (GET /api/audit?source=gateway&limit=1 returned none); run scripts/gateway_e2e.py first');

    await filterButton(page, 'Gateway').click();
    const row = page.locator('.activity-row').first();
    await expect(row.locator('.activity-chip')).toBeVisible();
    const label = await row.getAttribute('aria-label');
    expect(label).toBeTruthy();
    expect(label).toMatch(/,\s*[A-Z]+,\s*(allowed|denied),\s*status\s*\d{3}/);
  });

  // Breaks if: a denied gateway event hides whether a credential id was recorded.
  test('denied gateway detail shows the Kid row', async ({ page }) => {
    await openActivity(page);
    const denied = await auditTotal(page, 'source=gateway&decision=deny&limit=1');
    test.skip(denied === 0, 'No denied gateway rows on this stack; run scripts/gateway_e2e.py first');

    await filterButton(page, 'Gateway').click();
    await expect(page.locator('.activity-row .activity-chip').first()).toBeVisible();
    await filterButton(page, 'Deny').click();
    await expect(page.locator('.activity-row .activity-chip').first()).toHaveClass(/is-deny/);

    await page.locator('.activity-row').first().click();
    await expect(page).toHaveURL(/#\/activity\/\d+$/);
    await expect(page.getByRole('heading', { name: /^Event #\d+$/ })).toBeVisible();

    const kidRow = page
      .locator('.detail-grid > div')
      .filter({ has: page.getByText('Kid', { exact: true }) });
    await expect(kidRow).toBeVisible();
    const kidText = (await kidRow.locator('dd').innerText()).trim();
    expect(
      kidText.startsWith('STZ') || kidText.startsWith('not recorded'),
      `Kid value should be a credential id (STZ…) or "not recorded", got: ${kidText}`,
    ).toBeTruthy();
  });

  // Breaks if: keyboard users cannot skip navigation, or the skip link clobbers the hash route.
  test('skip link moves focus to main without changing the route', async ({ page }) => {
    await openActivity(page);
    await page.reload();
    await expect(page.getByRole('heading', { name: 'Recent activity' })).toBeVisible();

    await page.keyboard.press('Tab');
    const skipLink = page.getByRole('link', { name: 'Skip to content' });
    await expect(skipLink).toBeFocused();
    await expect(skipLink).toBeVisible();

    await page.keyboard.press('Enter');
    await expect(page.locator('#main-content')).toBeFocused();
    await expect(page).toHaveURL(/#\/activity\/?($|\?)/);
  });

  // Breaks if: Settings again presents open production items as completed checks.
  test('settings lists production items as open, not done', async ({ page }) => {
    await login(page);
    await page.getByRole('button', { name: 'Settings' }).click();
    await expect(page.getByRole('heading', { name: 'Before production' })).toBeVisible();
    await expect(page.locator('.check-list .check-row')).toHaveCount(6);
    await expect(page.getByText('open items, not completed checks')).toBeVisible();
    await expect(page.locator('.check-list svg.lucide-circle-check')).toHaveCount(0);
  });

  // Breaks if: the login page heading or primary action copy regresses.
  test('login page copy', async ({ page }) => {
    await page.goto('/');
    await expect(page.getByRole('heading', { name: 'Sign in to Storzo' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Sign in to console' })).toBeVisible();
  });
});
