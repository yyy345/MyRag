import { test, expect, readCaptcha } from './fixtures';

test('real login rejects a wrong password, then restores identity and logs out', async ({ page, account }) => {
  let captcha = await readCaptcha(page, () => page.goto('/analytics'));
  await expect(page).toHaveURL(/\/login\?redirect=/);
  await page.getByLabel('用户名 / 邮箱').fill(account.email);
  await page.getByLabel('密码', { exact: true }).fill('wrong-password');
  await page.getByLabel('验证码', { exact: true }).fill(captcha.answer);
  captcha = await readCaptcha(page, () => page.getByRole('button', { name: '登录', exact: true }).click());
  await expect(page.getByRole('alert')).toHaveText('用户名或密码错误');
  await page.getByLabel('密码', { exact: true }).fill(account.password);
  await page.getByLabel('验证码', { exact: true }).fill(captcha.answer);
  await page.getByRole('button', { name: '登录', exact: true }).click();
  await expect(page.getByRole('heading', { name: '数据统计' })).toBeVisible();
  await page.reload();
  await expect(page.locator('.account')).toContainText(account.username);
  await page.getByRole('button', { name: '退出登录' }).click();
  await expect(page).toHaveURL(/\/login$/);
  expect(await page.evaluate(() => localStorage.getItem('token'))).toBeNull();
  await page.goto('/');
  await expect(page).toHaveURL(/\/login/);
});

test('expired captcha fails and refresh permits a new attempt', async ({ page, account }) => {
  const captcha = await readCaptcha(page, () => page.goto('/login'));
  const { redis } = await import('./fixtures');
  await redis.del(`captcha:${captcha.id}`);
  await page.getByLabel('用户名 / 邮箱').fill(account.username);
  await page.getByLabel('密码', { exact: true }).fill(account.password);
  await page.getByLabel('验证码', { exact: true }).fill(captcha.answer);
  const next = await readCaptcha(page, () => page.getByRole('button', { name: '登录', exact: true }).click());
  await expect(page.getByRole('alert')).toHaveText('验证码错误或已过期');
  expect(next.id).not.toBe(captcha.id);
  await expect(page.getByLabel('验证码', { exact: true })).toHaveValue('');
});
