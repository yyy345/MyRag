import { randomUUID } from 'node:crypto';
import bcrypt from 'bcryptjs';
import { test, expect, readCaptcha, db } from './fixtures';

test('registration validates input, rejects duplicates and persists a hashed password', async ({ page, account }) => {
  const username = `registration_${randomUUID().slice(0, 8)}`;
  const password = 'Register-test-2026!';
  try {
    let captcha = await readCaptcha(page, () => page.goto('/register'));
    await page.getByLabel('用户名', { exact: true }).fill(account.username);
    await page.getByLabel('邮箱', { exact: true }).fill(account.email);
    await page.getByLabel('密码', { exact: true }).fill('123');
    expect(await page.getByLabel('密码', { exact: true }).evaluate((el: HTMLInputElement) => el.checkValidity())).toBe(false);
    await page.getByLabel('密码', { exact: true }).fill(password);
    await page.getByLabel('验证码', { exact: true }).fill(captcha.answer);
    captcha = await readCaptcha(page, () => page.getByRole('button', { name: '注册', exact: true }).click());
    await expect(page.getByRole('alert')).toHaveText('用户名或邮箱已存在');
    await page.getByLabel('用户名', { exact: true }).fill(username);
    await page.getByLabel('邮箱', { exact: true }).fill(`${username}@example.test`);
    await page.getByLabel('验证码', { exact: true }).fill(captcha.answer);
    await page.getByRole('button', { name: '注册', exact: true }).click();
    await expect(page.getByRole('heading', { name: '我的知识库', exact: true })).toBeVisible();
    const user = await db.user.findUniqueOrThrow({ where: { username } });
    expect(user.passwordHash).not.toBe(password);
    expect(await bcrypt.compare(password, user.passwordHash)).toBe(true);
    await page.getByRole('button', { name: '退出登录' }).click();
    await page.getByRole('link', { name: '立即注册' }).click();
    await expect(page.getByRole('heading', { name: '创建账号' })).toBeVisible();
    await page.getByRole('link', { name: '立即登录' }).click();
    await expect(page.getByRole('heading', { name: 'AI 知识库平台' })).toBeVisible();
  } finally { await db.user.deleteMany({ where: { username } }); }
});
