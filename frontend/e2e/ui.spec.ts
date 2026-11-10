import { test, expect } from '@playwright/test';

// 1x1 transparent PNG.
const DOT_PNG = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==',
  'base64',
);

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

test('console login, upload PNG, thumbnail, download, delete', async ({ page }) => {
  const password = process.env.E2E_ADMIN_PASSWORD;
  expect(password, 'E2E_ADMIN_PASSWORD must be set to run the UI spec').toBeTruthy();
  const bucket = `e2e-${Date.now().toString(36)}`;
  const key = 'e2e-dot.png';

  // Login through the UI form.
  await page.goto('/');
  await page.getByLabel('Username').fill('admin');
  await page.getByLabel('Password').fill(password!);
  await page.getByRole('button', { name: 'Sign in to console' }).click();
  await expect(page.getByRole('button', { name: 'Buckets & objects' })).toBeVisible();

  // "New bucket" lives on the Buckets page, not the dashboard.
  await page.getByRole('button', { name: 'Buckets & objects' }).click();

  // Create a bucket (an in-app dialog asks for the name).
  await page.getByRole('button', { name: 'New bucket' }).click();
  const dialog = page.getByTestId('app-dialog');
  await dialog.getByLabel('Name').fill(bucket);
  await dialog.getByRole('button', { name: 'Create bucket' }).click();
  await expect(page.getByRole('button', { name: bucket }).first()).toBeVisible();

  // Upload a small PNG via the hidden file input.
  await page.getByTestId('upload-input').setInputFiles({
    name: key,
    mimeType: 'image/png',
    buffer: DOT_PNG,
  });
  const row = page.locator('[data-testid="file-row"][data-key="' + key + '"]');
  await expect(row).toBeVisible({ timeout: 30_000 });
  await expect(page.getByTestId('upload-status')).toContainText('Uploaded');

  // The object list shows a thumbnail for the image.
  await expect(row.getByTestId('object-thumbnail')).toBeVisible({ timeout: 30_000 });

  // Download returns the bytes (context cookies carry the session).
  const dl = await page.request.get(
    `/api/objects/download?bucket=${encodeURIComponent(bucket)}&key=${encodeURIComponent(key)}`,
  );
  expect(dl.status()).toBe(200);
  expect(dl.headers()['content-type']).toContain('image/png');
  expect((await dl.body()).length).toBeGreaterThan(0);

  // Delete the object (an in-app dialog confirms first).
  await row.getByTestId('delete-object').click();
  await page.getByTestId('app-dialog').getByRole('button', { name: 'Delete', exact: true }).click();
  await expect(row).toHaveCount(0, { timeout: 30_000 });

  // Clean up the bucket.
  await page.getByRole('button', { name: 'Delete bucket' }).click();
  await page.getByTestId('app-dialog').getByRole('button', { name: 'Delete', exact: true }).click();
  await expect(page.getByRole('button', { name: bucket })).toHaveCount(0);
});
