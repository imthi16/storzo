import { test, expect } from '@playwright/test';

// Issues one read-only credential and checks the secret is shown once. Needs E2E_ADMIN_PASSWORD and a running stack.
const baseURL = process.env.PLAYWRIGHT_BASE_URL ?? 'http://127.0.0.1:8088';

test('issue a read-only credential and show the secret once', async ({ page }) => {
  const password = process.env.E2E_ADMIN_PASSWORD;
  expect(password, 'E2E_ADMIN_PASSWORD must be set').toBeTruthy();
  try { const r = await fetch(`${baseURL}/api/health/live`, { signal: AbortSignal.timeout(5000) }); test.skip(!r.ok, 'stack not running'); } catch { test.skip(true, 'stack not running'); }

  const bucket = `cred-${Date.now().toString(36)}`;
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

  await page.getByRole('button', { name: 'Credentials' }).click();
  await expect(page).toHaveURL(/#\/credentials$/);
  await page.getByRole('combobox', { name: 'Bucket' }).selectOption(bucket);
  await page.getByLabel('Prefix').fill('agents');
  await page.getByRole('button', { name: 'Issue credential' }).click();
  await expect(page.getByRole('alert')).toContainText('Prefix must end with /.');

  await page.getByLabel('Prefix').fill('agents/');
  await page.getByRole('radio', { name: 'Read only' }).check();
  await page.getByRole('radio', { name: '15 min' }).check();
  await page.getByRole('button', { name: 'Issue credential' }).click();

  const once = page.getByTestId('credential-once');
  await expect(once).toContainText('cannot be shown again');
  const secret = (await page.getByTestId('credential-secret').innerText()).trim();
  expect(secret.length).toBeGreaterThan(16);
  await expect(page.getByTestId('credential-env')).toContainText('STORZO_MODE=ro');
  await expect(page.getByTestId('credential-env')).toContainText(`STORZO_BUCKET=${bucket}`);
  await expect(page.getByTestId('credential-env')).toContainText('STORZO_PREFIX=agents/');
  await expect(page.getByTestId('credential-mcp')).toContainText('storzo-mcp');
  expect(page.url()).not.toContain(secret);
  const stored = await page.evaluate(() => JSON.stringify(localStorage));
  expect(stored).not.toContain(secret);

  await page.getByRole('button', { name: 'Overview' }).click();
  await page.getByRole('button', { name: 'Credentials' }).click();
  await expect(page.getByTestId('credential-once')).toHaveCount(0);
  await expect(page.getByText(secret)).toHaveCount(0);

  const me = await (await page.request.get('/api/auth/me')).json();
  const gone = await page.request.delete(`/api/buckets/${bucket}`, { headers: { 'X-CSRF-Token': me.csrf } });
  expect(gone.ok()).toBeTruthy();
});
