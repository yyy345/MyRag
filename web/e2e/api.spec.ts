import { test, expect } from '@playwright/test';

// Requires the real Express API on API_PROXY_TARGET (default localhost:3000).
test('Vue API client talks to Express through the Vite proxy', async ({ page }) => {
  await page.goto('/login');
  await expect(page.getByRole('heading', { name: 'AI 知识库平台' })).toBeVisible();
  await expect(page.getByRole('img', { name: '数学验证码' })).toBeVisible();
  const result = await page.evaluate(async () => {
    // This import is served by Vite and runs the production client in the browser.
    const modulePath = '/src/api/client.ts';
    const { api } = await import(/* @vite-ignore */ modulePath);
    const captcha = await api('/auth/captcha');
    localStorage.setItem('token', 'invalid-test-token');
    let unauthorized = 0;
    window.addEventListener('auth:unauthorized', () => unauthorized++, { once: true });
    let failure;
    try { await api('/auth/me'); } catch (error) { failure = { status: (error as any).status, code: (error as any).code }; }
    return { hasId: typeof captcha.id === 'string', hasSvg: captcha.svg.includes('<svg'), failure, unauthorized, token: localStorage.getItem('token') };
  });
  expect(result).toEqual({ hasId: true, hasSvg: true, failure: { status: 401, code: 1001 }, unauthorized: 1, token: null });
});
