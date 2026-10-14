import { test, expect, db, redis } from './fixtures';
import amqp from 'amqplib';

test('real worker failure invalidates cached pending status without test-side cache deletion', async ({ signedInPage: page, account }) => {
  test.skip(process.env.MIGRATION_EXTERNAL_AI === '1', 'Failure injection requires local-only provider adapters.');
  const connection = await amqp.connect('amqp://guest:guest@127.0.0.1:55673');
  try {
    const channel = await connection.createConfirmChannel();
    await channel.assertQueue('document_processing', { durable: true });
    for (const text of ['合成故障测试', '']) {
      const kb = await db.knowledgeBase.create({ data: { userId: account.id, name: '任务失败缓存测试' } });
      const entry = await db.knowledgeEntry.create({ data: { kbId: kb.id, title: text || '空文本任务', content: text, processingStatus: 'PENDING' } });
      await page.goto(`/knowledge-bases/${kb.id}`);
      await page.getByRole('button', { name: '刷新', exact: true }).click();
      // Populate the real list cache before the actual RabbitMQ consumer fails.
      const row = page.getByRole('row').filter({ hasText: entry.title });
      await expect(row).toContainText('待处理');
      expect(await redis.exists(`entry:${kb.id}:1:10`)).toBe(1);
      channel.sendToQueue('document_processing', Buffer.from(JSON.stringify({ entryId: entry.id, text })), { persistent: true });
      await channel.waitForConfirms();
      await expect.poll(async () => (await db.knowledgeEntry.findUniqueOrThrow({ where: { id: entry.id } })).processingStatus).toBe('FAILED');
      await expect(row).toContainText('失败', { timeout: 10000 });
      await row.getByRole('button', { name: '详情', exact: true }).click();
      await expect(page.getByRole('dialog')).toContainText(text ? '外部服务未启用' : 'Document text is empty');
      await page.getByRole('button', { name: '关闭弹窗' }).click();
      // Each fixture gets its own cache; no cache invalidation can mask the consumer regression.
    }
  } finally { await connection.close(); }
});

test('upload validates type, handles parse errors and deduplicates a real TXT', async ({ signedInPage: page, account }) => {
  const kb = await db.knowledgeBase.create({ data: { userId: account.id, name: '文档入库测试' } });
  await page.goto(`/knowledge-bases/${kb.id}`);
  await page.getByRole('button', { name: '上传文件', exact: true }).click();
  const input = page.getByLabel('选择文件', { exact: true });
  await input.setInputFiles({ name: 'unsupported.exe', mimeType: 'application/octet-stream', buffer: Buffer.from('test') });
  await page.getByRole('button', { name: '上传', exact: true }).click();
  await expect(page.getByRole('alert')).toHaveText('不支持的文件格式');
  await input.setInputFiles({ name: 'large.txt', mimeType: 'text/plain', buffer: Buffer.alloc(20 * 1024 * 1024 + 1, 65) });
  await page.getByRole('button', { name: '上传', exact: true }).click();
  await expect(page.getByRole('alert')).toHaveText('文档最大 20MB');
  await input.setInputFiles({ name: 'empty.txt', mimeType: 'text/plain', buffer: Buffer.from(' \n') });
  await page.getByRole('button', { name: '上传', exact: true }).click();
  await expect(page.getByRole('alert')).toContainText('文档解析失败');
  const file = { name: '迁移文档.txt', mimeType: 'text/plain', buffer: Buffer.from('这是独立测试环境的合成资料。知识库支持文档上传和问答。') };
  await input.setInputFiles(file);
  await page.route(`**/knowledge-bases/${kb.id}/entries/upload`, route => route.abort('failed'), { times: 1 });
  await page.getByRole('button', { name: '上传', exact: true }).click();
  await expect(page.getByRole('alert')).toContainText('网络连接失败');
  expect(await db.knowledgeEntry.count({ where: { kbId: kb.id } })).toBe(0);
  // The failed request keeps the selection and re-enables submission.
  await page.getByRole('button', { name: '上传', exact: true }).click();
  await expect(page.getByRole('status').filter({ hasText: '已提交文件' })).toBeVisible();
  await input.setInputFiles(file);
  await page.getByRole('button', { name: '上传', exact: true }).click();
  await expect(page.getByRole('status').filter({ hasText: '已提交文件' })).toBeVisible();
  expect(await db.knowledgeEntry.count({ where: { kbId: kb.id } })).toBe(1);
  await page.getByRole('button', { name: '关闭弹窗' }).click();
  const row = page.getByRole('row').filter({ hasText: '迁移文档.txt' });
  await expect(row).toBeVisible();
  const entry = await db.knowledgeEntry.findFirstOrThrow({ where: { kbId: kb.id } });
  expect(entry.content).toContain('合成资料');
});

test('external integration produces real embeddings for the uploaded fixture', async ({ signedInPage: page, account }) => {
  test.skip(process.env.MIGRATION_EXTERNAL_AI !== '1', 'Requires explicitly authorized external model integration; local tests do not count as index validation.');
  test.setTimeout(120000);
  const kb = await db.knowledgeBase.create({ data: { userId: account.id, name: '真实向量集成测试' } });
  await page.goto(`/knowledge-bases/${kb.id}`);
  await page.getByRole('button', { name: '上传文件', exact: true }).click();
  await page.getByLabel('选择文件', { exact: true }).setInputFiles({ name: '向量测试.txt', mimeType: 'text/plain', buffer: Buffer.from('这是独立测试环境的合成资料。知识库支持文档上传和问答。') });
  await page.getByRole('button', { name: '上传', exact: true }).click();
  await expect(page.getByRole('status').filter({ hasText: '已提交文件' })).toBeVisible();
  await page.getByRole('button', { name: '关闭弹窗' }).click();
  const row = page.getByRole('row').filter({ hasText: '向量测试.txt' });
  // A provider failure is terminal, not a reason to wait for the entire UI timeout.
  await expect.poll(async () => (await db.knowledgeEntry.findFirstOrThrow({ where: { kbId: kb.id } })).processingStatus, { timeout: 90000 }).toMatch(/^(COMPLETED|FAILED)$/);
  const outcome = await db.knowledgeEntry.findFirstOrThrow({ where: { kbId: kb.id } });
  expect(outcome.processingStatus, '真实 Embedding 必须成功；FAILED 不能作为通过').toBe('COMPLETED');
  await expect(row).toContainText('已完成', { timeout: 90000 });
  const entry = await db.knowledgeEntry.findFirstOrThrow({ where: { kbId: kb.id } });
  expect(entry.processingStatus).toBe('COMPLETED');
  expect(await db.knowledgeChunk.count({ where: { entryId: entry.id } })).toBeGreaterThan(0);
});

test('pending status polls, displays failure detail and stops after leaving the page', async ({ signedInPage: page, account }) => {
  const kb = await db.knowledgeBase.create({ data: { userId: account.id, name: '轮询测试' } });
  const entry = await db.knowledgeEntry.create({ data: { kbId: kb.id, title: '等待任务', content: '测试', processingStatus: 'PENDING' } });
  let requests = 0;
  page.on('request', request => { if (request.url().includes(`/knowledge-bases/${kb.id}/entries?`)) requests++; });
  await page.goto(`/knowledge-bases/${kb.id}`);
  await expect(page.getByRole('cell', { name: '待处理', exact: true })).toBeVisible();
  await expect.poll(() => requests, { timeout: 10000 }).toBeGreaterThan(1);
  // Simulate a worker failure in the real test DB, invalidating only this fixture's list cache.
  await db.knowledgeEntry.update({ where: { id: entry.id }, data: { processingStatus: 'FAILED', processingMessage: '测试任务失败，请检查文档' } });
  await redis.del(`entry:${kb.id}:1:10`);
  await expect(page.getByRole('cell', { name: '失败', exact: true })).toBeVisible({ timeout: 10000 });
  await page.getByRole('button', { name: '详情', exact: true }).click();
  await expect(page.getByRole('dialog', { name: '条目详情' })).toContainText('测试任务失败，请检查文档');
  await page.getByRole('button', { name: '关闭弹窗' }).click();
  // Navigate through the SPA while pending, so the unmount hook must stop its timer.
  await db.knowledgeEntry.update({ where: { id: entry.id }, data: { processingStatus: 'PENDING' } });
  await redis.del(`entry:${kb.id}:1:10`);
  await page.getByRole('button', { name: '刷新', exact: true }).click();
  await expect(page.getByRole('cell', { name: '待处理', exact: true })).toBeVisible();
  await page.getByRole('link', { name: '返回知识库', exact: true }).click();
  await expect(page.getByRole('heading', { name: '我的知识库', exact: true })).toBeVisible();
  const afterNavigation = requests;
  await page.waitForTimeout(3500); // Observe longer than one polling interval.
  expect(requests).toBe(afterNavigation);
});
