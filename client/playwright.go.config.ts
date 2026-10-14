import { defineConfig } from '@playwright/test';

process.env.MIGRATION_BACKEND = 'go';
export default defineConfig({
  testDir: './e2e',
  testMatch: 'go-workflow.spec.ts',
  workers: 1,
  fullyParallel: false,
  timeout: 60000,
  use: {
    baseURL: 'http://127.0.0.1:5173',
    channel: process.env.PLAYWRIGHT_CHANNEL || 'msedge',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  webServer: {
    command: 'node ../node_modules/vite/bin/vite.js --host 127.0.0.1 --port 5173 --strictPort',
    url: 'http://127.0.0.1:5173',
    reuseExistingServer: false,
    env: { API_PROXY_TARGET: 'http://127.0.0.1:3180' },
    timeout: 30000,
  },
});
