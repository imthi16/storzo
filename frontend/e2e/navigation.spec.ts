import { test, expect } from '@playwright/test';

// Own scratch bucket with 30 tiny files: 2 pages at 20 rows. Needs E2E_ADMIN_PASSWORD and a running stack.
const baseURL = process.env.PLAYWRIGHT_BASE_URL ?? 'http://127.0.0.1:8088';

test('pagination, drag and drop, activity detail and breadcrumbs', async ({ page }) => {
  const password = process.env.E2E_ADMIN_PASSWORD;
  expect(password, 'E2E_ADMIN_PASSWORD must be set').toBeTruthy();
  try { const r = await fetch(`${baseURL}/api/health/live`, { signal: AbortSignal.timeout(5000) }); test.skip(!r.ok, 'stack not running'); } catch { test.skip(true, 'stack not running'); }

  const bucket = `pg-${Date.now().toString(36)}`;
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto('/');
  await page.getByLabel('Username').fill('admin');
  await page.getByLabel('Password').fill(password!);
  await page.getByRole('button', { name: 'Sign in to console' }).click();
  await page.getByRole('button', { name: 'Buckets & objects' }).click();
  await page.getByRole('button', { name: 'New bucket' }).click();
  await page.getByTestId('app-dialog').getByLabel(/Name/).fill(bucket);
  await page.getByTestId('app-dialog').getByRole('button', { name: 'Create bucket' }).click();
  await expect(page.locator('.bucket-tab.selected', { hasText: bucket })).toBeVisible();

  const files = Array.from({ length: 30 }, (_, i) => ({ name: `f-${String(i + 1).padStart(2, '0')}.txt`, mimeType: 'text/plain', buffer: Buffer.from(`file ${i + 1}`) }));
  await page.getByTestId('upload-input').setInputFiles(files);
  await expect(page.getByTestId('upload-status')).toContainText('Uploaded', { timeout: 60_000 });

  // pagination
  const keys = () => page.getByTestId('file-row').evaluateAll(els => els.map(e => e.getAttribute('data-key')));
  await expect(page.getByTestId('file-row')).toHaveCount(10);
  await page.getByLabel('Rows per page').first().selectOption('20');
  await expect(page.getByTestId('file-row')).toHaveCount(20);
  await expect(page.locator('.pager-page')).toHaveText('Page 1');
  await expect(page.getByTestId('pager-prev')).toBeDisabled();
  const first = await keys();
  await page.getByTestId('pager-next').click();
  await expect(page.getByTestId('file-row')).toHaveCount(10);
  expect((await keys()).filter(k => first.includes(k))).toHaveLength(0);
  await expect(page.getByTestId('pager-next')).toBeDisabled();
  await page.getByTestId('pager-prev').click();
  await expect(page.getByTestId('file-row')).toHaveCount(20);

  // the page itself never scrolls; the table scrolls inside its panel
  const m = await page.evaluate(() => { const s = document.querySelector('.object-scroll')!; return { doc: document.documentElement.scrollHeight, win: innerHeight, content: s.scrollHeight, shown: s.clientHeight }; });
  expect(m.doc).toBeLessThanOrEqual(m.win + 1);
  expect(m.content).toBeGreaterThan(m.shown);

  // drag and drop: overlay while dragging, upload progress UI after the drop
  const dt = await page.evaluateHandle(() => { const d = new DataTransfer(); d.items.add(new File(['dropped'], 'dropped.txt', { type: 'text/plain' })); return d; });
  await page.locator('.objects-panel').dispatchEvent('dragenter', { dataTransfer: dt });
  await expect(page.locator('.drop-overlay')).toContainText('Drop to upload');
  await page.locator('.objects-panel').dispatchEvent('drop', { dataTransfer: dt });
  await expect(page.locator('.drop-overlay')).toHaveCount(0);
  await expect(page.getByTestId('upload-status')).toContainText('dropped.txt');

  // activity log: pagination, detail page, breadcrumbs that act as links, deep links
  await page.getByRole('button', { name: 'Activity log' }).first().click();
  await expect(page.locator('.pager-summary')).toContainText('Showing 1-20 of');
  await page.getByTestId('pager-next').click();
  await expect(page.locator('.pager-page')).toHaveText('Page 2');
  await page.getByTestId('pager-prev').click();
  await page.locator('.activity-row').first().click();
  await expect(page).toHaveURL(/#\/activity\/\d+$/);
  await expect(page.getByRole('heading', { name: /^Event #\d+$/ })).toBeVisible();
  await expect(page.locator('.detail-grid').first()).toContainText('Actor');
  await page.locator('.breadcrumbs').getByRole('button', { name: 'Activity log' }).click();
  await expect(page).toHaveURL(/#\/activity$/);
  await page.locator('.breadcrumbs').getByRole('button', { name: 'Workspace' }).click();
  await expect(page).toHaveURL(/#\/overview$/);
  await page.goto('/#/activity/99999999');
  await expect(page.getByText('no longer exists')).toBeVisible();

  // clean up: delete every object through the API (deterministic), then the bucket through the UI
  const me = await (await page.request.get('/api/auth/me')).json();
  const listing = await (await page.request.get(`/api/buckets/${bucket}/objects?limit=500`)).json();
  for (const o of listing.items) {
    const r = await page.request.delete(`/api/objects?bucket=${bucket}&key=${encodeURIComponent(o.key)}`, { headers: { 'X-CSRF-Token': me.csrf } });
    expect(r.ok()).toBeTruthy();
  }
  await page.goto('/#/buckets');
  await page.locator('.bucket-tab', { hasText: bucket }).click();
  await expect(page.getByText('This bucket is empty')).toBeVisible();
  await page.getByRole('button', { name: 'Delete bucket' }).click();
  await page.getByTestId('app-dialog').getByRole('button', { name: 'Delete', exact: true }).click();
  await expect(page.locator('.bucket-tab', { hasText: bucket })).toHaveCount(0);
});
