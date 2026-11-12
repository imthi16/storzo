import { defineConfig } from 'vitest/config';

// Unit tests live next to the code; e2e/ is Playwright's.
export default defineConfig({ test: { include: ['src/**/*.test.{ts,tsx}'] } });
