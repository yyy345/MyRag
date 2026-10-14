import { test, expect } from './fixtures';

test('Vue boots in a real browser without runtime errors', async ({ signedInPage: page }) => {
  const errors: string[] = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.goto('/');
  await expect(page.getByRole('heading', { name: '我的知识库', exact: true })).toBeVisible();
  await page.reload();
  await expect(page.getByRole('heading', { name: '我的知识库', exact: true })).toBeVisible();
  await page.getByRole('link', { name: '数据统计' }).click();
  await expect(page.getByRole('heading', { name: '数据统计' })).toBeVisible();
  await page.goto('/knowledge-bases/test/chat/session');
  await page.reload();
  await expect(page.getByRole('heading', { name: '对话问答' })).toBeVisible();
  await page.goto('/unknown');
  await expect(page.getByRole('heading', { name: '页面不存在' })).toBeVisible();
  await page.getByRole('link', { name: '返回知识库' }).click();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole('button', { name: '导航菜单' }).click();
  await page.getByRole('link', { name: '数据统计' }).click();
  await expect(page.getByRole('heading', { name: '数据统计' })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  expect(errors).toEqual([]);
});
