import { test, expect, db } from './fixtures';
import { bluePng } from './blue-png';
import { createHash } from 'node:crypto';

test('image batch preserves an existing result, shows a failed item and permits delete then reupload', async ({ signedInPage: page, account }) => {
  test.skip(process.env.MIGRATION_EXTERNAL_AI === '1', 'Uses local-only provider failure injection.');
  const kb = await db.knowledgeBase.create({ data: { userId: account.id, name: '图片批次失败测试' } });
  const existingImage = { name: 'existing.png', mimeType: 'image/png', buffer: bluePng() };
  const failedImage = { name: 'retry.png', mimeType: 'image/png', buffer: bluePng(32) };
  // Seed a historical COMPLETED entry only to exercise deduplication, not model success.
  const existing = await db.knowledgeEntry.create({ data: { kbId: kb.id, title: existingImage.name, sourceFileName: existingImage.name, content: '已有图片描述', type: 'FILE', processingStatus: 'COMPLETED', sourceFileHash: createHash('sha256').update(existingImage.buffer).digest('hex') } });
  await page.goto(`/knowledge-bases/${kb.id}`);
  const submit = async (files: typeof existingImage[]) => {
    await page.getByRole('button', { name: '上传文件', exact: true }).click();
    await page.getByLabel('上传类型', { exact: true }).selectOption('image');
    await page.getByLabel('选择文件', { exact: true }).setInputFiles(files);
    await page.getByRole('button', { name: '上传', exact: true }).click();
    await expect(page.getByRole('status').filter({ hasText: '已提交文件' })).toBeVisible();
    await page.getByRole('button', { name: '关闭弹窗' }).click();
  };
  await submit([existingImage, failedImage]);
  await expect(page.getByRole('row').filter({ hasText: existingImage.name })).toContainText('已完成');
  const failedRow = page.getByRole('row').filter({ hasText: failedImage.name });
  await expect(failedRow).toContainText('失败');
  const failed = await db.knowledgeEntry.findFirstOrThrow({ where: { kbId: kb.id, title: failedImage.name } });
  await failedRow.getByRole('button', { name: '详情', exact: true }).click();
  await expect(page.getByRole('dialog')).toContainText('外部服务未启用');
  await expect(page.getByRole('dialog')).toContainText('先删除此失败条目，再重新上传原文件');
  await page.getByRole('button', { name: '关闭弹窗' }).click();
  await submit([failedImage]);
  expect(await db.knowledgeEntry.count({ where: { kbId: kb.id } })).toBe(2);
  // Legacy deduplication reuses FAILED entries; retry is explicitly delete then reupload.
  await failedRow.getByRole('button', { name: '删除', exact: true }).click();
  await page.getByRole('button', { name: '确认删除', exact: true }).click();
  await expect(page.getByRole('dialog')).not.toBeVisible();
  await expect(failedRow).not.toBeVisible();
  await submit([failedImage]);
  await expect(failedRow).toContainText('失败');
  const retried = await db.knowledgeEntry.findFirstOrThrow({ where: { kbId: kb.id, title: failedImage.name } });
  expect(retried.id).not.toBe(failed.id);
  expect(await db.knowledgeEntry.findUnique({ where: { id: failed.id } })).toBeNull();
  expect((await db.knowledgeEntry.findUniqueOrThrow({ where: { id: existing.id } })).processingStatus).toBe('COMPLETED');
});

test('image selection enforces size and batch count before network submission', async ({ signedInPage: page, account }) => {
  const kb = await db.knowledgeBase.create({ data: { userId: account.id, name: '图片限制测试' } });
  await page.goto(`/knowledge-bases/${kb.id}`);
  await page.getByRole('button', { name: '上传文件', exact: true }).click();
  await page.getByLabel('上传类型', { exact: true }).selectOption('image');
  const input = page.getByLabel('选择文件', { exact: true });
  await input.setInputFiles({ name: 'large.png', mimeType: 'image/png', buffer: Buffer.alloc(5 * 1024 * 1024 + 1) });
  await page.getByRole('button', { name: '上传', exact: true }).click();
  await expect(page.getByRole('alert')).toHaveText('单张图片最大 5MB');
  await input.setInputFiles(Array.from({ length: 11 }, (_, i) => ({ name: `${i}.png`, mimeType: 'image/png', buffer: bluePng() })));
  await page.getByRole('button', { name: '上传', exact: true }).click();
  await expect(page.getByRole('alert')).toHaveText('每次最多上传 10 张图片');
  expect(await db.knowledgeEntry.count({ where: { kbId: kb.id } })).toBe(0);
});

test('external integration stores a synthetic image and generates its description', async ({ signedInPage: page, account }) => {
  test.skip(process.env.MIGRATION_EXTERNAL_AI !== '1', 'Explicit authorization for the configured OSS bucket and Bailian is required.');
  test.setTimeout(150000);
  const kb = await db.knowledgeBase.create({ data: { userId: account.id, name: '真实图片集成测试' } });
  try {
    await page.goto(`/knowledge-bases/${kb.id}`);
    await page.getByRole('button', { name: '上传文件', exact: true }).click();
    await page.getByLabel('上传类型', { exact: true }).selectOption('image');
    await page.getByLabel('选择文件', { exact: true }).setInputFiles({ name: 'blue-square.png', mimeType: 'image/png', buffer: bluePng() });
    await page.getByRole('button', { name: '上传', exact: true }).click();
    await expect(page.getByRole('status').filter({ hasText: '已提交文件' })).toBeVisible({ timeout: 65000 });
    await page.getByRole('button', { name: '关闭弹窗' }).click();
    const row = page.getByRole('row').filter({ hasText: 'blue-square.png' });
    await expect.poll(async () => (await db.knowledgeEntry.findFirstOrThrow({ where: { kbId: kb.id } })).processingStatus, { timeout: 90000 }).toMatch(/^(COMPLETED|FAILED)$/);
    const outcome = await db.knowledgeEntry.findFirstOrThrow({ where: { kbId: kb.id } });
    expect(outcome.processingStatus, '真实视觉与索引必须成功；FAILED 不能作为通过').toBe('COMPLETED');
    await expect(row).toContainText('已完成', { timeout: 90000 });
    const entry = await db.knowledgeEntry.findFirstOrThrow({ where: { kbId: kb.id } });
    expect(entry.content).toContain('![](');
    expect(entry.content.split('![](')[0]!.trim().length).toBeGreaterThan(0);
    await row.getByRole('button', { name: '预览', exact: true }).click();
    await expect(page.getByRole('dialog', { name: '预览知识条目' }).locator('img')).toBeVisible();
  } finally {
    // Await deletion of exact objects created by this isolated fixture, including failed uploads.
    const { config } = await import('dotenv');
    const { resolve } = await import('node:path');
    const OSS = (await import('ali-oss')).default;
    config({ path: resolve('../server/.env') });
    const client = new OSS({ accessKeyId: process.env.OSS_ACCESS_KEY_ID!, accessKeySecret: process.env.OSS_ACCESS_KEY_SECRET!, bucket: process.env.OSS_BUCKET!, region: process.env.OSS_REGION || 'oss-cn-hangzhou' });
    const entries = await db.knowledgeEntry.findMany({ where: { kbId: kb.id, kb: { userId: account.id }, sourceFileName: 'blue-square.png' } });
    for (const entry of entries) {
      const key = `ai-library/images/${account.id}/${entry.id}.png`;
      try { await client.delete(key); }
      catch { throw new Error(`测试对象清理失败：${key}`); }
    }
  }
});
