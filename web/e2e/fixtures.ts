import { test as base, expect, type Page } from '@playwright/test';
import { PrismaClient } from '@prisma/client';
import bcrypt from 'bcryptjs';
import jwt from 'jsonwebtoken';
import Redis from 'ioredis';
import { randomUUID } from 'node:crypto';

// Hard-coded isolated test endpoints: never derive destructive test targets from server/.env.
const testDatabase = process.env.MIGRATION_TEST_DATABASE_URL || 'postgresql://postgres:migration-test-only@127.0.0.1:55432/migration_test';
const target = new URL(testDatabase);
if (target.hostname !== '127.0.0.1' || target.port !== '55432' || !/^\/migration_(?:test|c26_[a-z0-9_]+)$/.test(target.pathname)) throw new Error('Tests require the isolated migration database');
export const db = new PrismaClient({ datasources: { db: { url: testDatabase } } });
const testSecret = process.env.MIGRATION_BACKEND === 'go' ? 'go-local-migration-test-secret-at-least-32-characters' : 'local-migration-test-only';
export const redis = new Redis('redis://127.0.0.1:56380', { lazyConnect: true, maxRetriesPerRequest: 1 });
type Account = { id: string; username: string; email: string; password: string; token: string };
export const test = base.extend<{ account: Account; signedInPage: Page }, { resources: void }>({
  resources: [async ({}, use) => {
    try { await use(); } finally { await db.$disconnect(); redis.disconnect(); }
  }, { scope: 'worker', auto: true }],
  account: async ({}, use) => {
    const username = `migration_${randomUUID().slice(0, 8)}`;
    const password = 'Migration-test-2026!';
    const user = await db.user.create({ data: { username, email: `${username}@example.test`, passwordHash: await bcrypt.hash(password, 10) } });
    try { await use({ ...user, password, token: jwt.sign({ id: user.id, username }, testSecret, { expiresIn: '1h' }) }); }
    finally { await db.user.deleteMany({ where: { id: user.id } }); }
  },
  signedInPage: async ({ page, account }, use) => {
    await page.addInitScript(token => localStorage.setItem('token', token), account.token);
    await page.goto('/');
    await expect(page.getByRole('heading', { name: '我的知识库', exact: true })).toBeVisible();
    await use(page);
  },
});
export { expect };

export async function readCaptcha(page: Page, action: () => Promise<unknown>) {
  const responsePromise = page.waitForResponse(r => r.url().endsWith('/api/auth/captcha') && r.status() === 200);
  await action();
  const { data } = await (await responsePromise).json();
  let answer: string | null = null;
  await expect.poll(async () => { answer = await redis.get(`captcha:${data.id}`); return answer; }).not.toBeNull();
  return { id: data.id as string, answer: JSON.parse(answer!) as string };
}
