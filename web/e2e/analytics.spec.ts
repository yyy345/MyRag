import { test, expect, db, redis } from './fixtures';
import { randomUUID } from 'node:crypto';

test('analytics aggregates real hits, renders charts and refreshes without leaking timers', async ({ signedInPage: page, account }, testInfo) => {
  const kb = await db.knowledgeBase.create({ data: { userId: account.id, name: '统计验收库' } });
  const entry = await db.knowledgeEntry.create({ data: { kbId: kb.id, title: '热门合成资料', content: '合成资料', processingStatus: 'COMPLETED' } });
  await db.chatSession.create({ data: { kbId: kb.id, userId: account.id, title: '统计会话' } });
  const chunkId = randomUUID(); const vector = `[${Array(1024).fill(0.01).join(',')}]`;
  await db.$executeRaw`INSERT INTO knowledge_chunk (id, entry_id, chunk_index, content, embedding, hit_count) VALUES (${chunkId}::uuid, ${entry.id}::uuid, 0, '合成资料', ${vector}::vector, 7)`;
  await page.route('**/analytics/overview', route => route.abort('failed'), { times: 1 });
  await page.goto('/analytics');
  await expect(page.getByRole('alert')).toContainText('网络连接失败');
  await page.getByRole('button', { name: '重试', exact: true }).click();
  const table = page.getByRole('table', { name: '热门条目明细' });
  await expect(table.getByRole('row').filter({ hasText: '热门合成资料' })).toContainText('7');
  await expect(page.locator('.summary-grid dd')).toHaveText(['1', '1', '1']);
  await expect(page.locator('.analytics-chart svg')).toHaveCount(2);
  await page.screenshot({ path: testInfo.outputPath('analytics.png'), fullPage: true });
  await db.$executeRaw`UPDATE knowledge_chunk SET hit_count=11 WHERE id=${chunkId}::uuid`;
  // Expire only this fixture's cached overview to validate refresh against changed DB data.
  await redis.del(`analytics:overview:${account.id}`);
  await page.getByRole('button', { name: '刷新统计', exact: true }).click();
  await expect(table.getByRole('row').filter({ hasText: '热门合成资料' })).toContainText('11');
  await page.clock.install(); let requests = 0;
  page.on('request', request => { if (request.url().endsWith('/analytics/overview')) requests++; });
  await page.getByRole('button', { name: '刷新统计', exact: true }).click();
  await expect(page.getByRole('button', { name: '刷新统计', exact: true })).toBeEnabled();
  await page.clock.runFor(300010);
  await expect.poll(() => requests).toBeGreaterThan(1);
  await page.getByRole('link', { name: '我的知识库', exact: true }).click();
  await expect(page.getByRole('heading', { name: '我的知识库' })).toBeVisible();
  const before = requests; await page.clock.runFor(300010); expect(requests).toBe(before);
});

test('analytics has explicit empty state and stays within a narrow viewport', async ({ signedInPage: page }) => {
  await page.goto('/analytics');
  await expect(page.getByText('暂无检索命中记录。')).toBeVisible();
  await expect(page.locator('.summary-grid dd')).toHaveText(['0', '0', '0']);
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});
