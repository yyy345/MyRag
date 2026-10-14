import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  workers: 1,
  use: {
    baseURL: 'http://127.0.0.1:5174',
    channel: process.env.PLAYWRIGHT_CHANNEL || 'msedge',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  webServer: [{
    command: 'node --import tsx ../scripts/legacy-test-server.cjs',
    url: 'http://127.0.0.1:3100/api/auth/captcha',
    reuseExistingServer: !process.env.CI,
    timeout: 30000,
  }, {
    command: 'node ../node_modules/vite/bin/vite.js --host 127.0.0.1 --port 5174 --strictPort',
    url: 'http://127.0.0.1:5174',
    reuseExistingServer: !process.env.CI,
    env: { API_PROXY_TARGET: 'http://127.0.0.1:3100' },
    timeout: 30000,
  }],
});
