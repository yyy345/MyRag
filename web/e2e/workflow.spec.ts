import { randomUUID } from 'node:crypto';
import { test, expect, db, readCaptcha } from './fixtures';

test('external integration runs registration through indexing, chat, extraction and analytics', async ({ page }, testInfo) => {
  test.skip(process.env.MIGRATION_EXTERNAL_AI !== '1', 'Real model workflow is explicitly opt-in.');
  test.setTimeout(240000);
  const username = `workflow_${randomUUID().slice(0, 8)}`;
  const source = '这是独立测试环境的合成资料。知识库支持文档上传和问答。';
  try {
    const captcha = await readCaptcha(page, () => page.goto('/register'));
    await page.getByLabel('用户名', { exact: true }).fill(username);
    await page.getByLabel('邮箱', { exact: true }).fill(`${username}@example.test`);
    await page.getByLabel('密码', { exact: true }).fill('Workflow-test-2026!');
    await page.getByLabel('验证码', { exact: true }).fill(captcha.answer);
    await page.getByRole('button', { name: '注册', exact: true }).click();
    await expect(page.getByRole('heading', { name: '我的知识库', exact: true })).toBeVisible();
    await page.getByRole('button', { name: '新建知识库', exact: true }).click();
    await page.getByLabel('名称', { exact: true }).fill('全流程合成资料库');
    await page.getByLabel('回答要求（可选）').fill('请仅依据资料，围绕资料来源、文档上传和知识问答整理一份不少于300字的说明，并标注引用；不要虚构具体文件格式、模型或部署细节。');
    await page.getByRole('button', { name: '保存', exact: true }).click();
    const kb = await db.knowledgeBase.findFirstOrThrow({ where: { user: { username } } });
    await page.getByRole('article', { name: '全流程合成资料库', exact: true }).getByRole('heading').getByRole('link').click();
    await page.getByRole('button', { name: '上传文件', exact: true }).click();
    await page.getByLabel('选择文件', { exact: true }).setInputFiles({ name: '全流程合成资料.txt', mimeType: 'text/plain', buffer: Buffer.from(source) });
    await page.getByRole('button', { name: '上传', exact: true }).click();
    await expect(page.getByRole('status').filter({ hasText: '已提交文件' })).toBeVisible();
    await page.getByRole('button', { name: '关闭弹窗' }).click();
    await expect.poll(async () => (await db.knowledgeEntry.findFirstOrThrow({ where: { kbId: kb.id } })).processingStatus, { timeout: 90000 }).toMatch(/^(COMPLETED|FAILED)$/);
    expect((await db.knowledgeEntry.findFirstOrThrow({ where: { kbId: kb.id } })).processingStatus, '真实索引失败时不能继续问答/提炼').toBe('COMPLETED');
    await expect(page.getByRole('row').filter({ hasText: '全流程合成资料.txt' })).toContainText('已完成');
    await page.getByRole('link', { name: '开始问答', exact: true }).click();
    await page.getByLabel('问题', { exact: true }).fill(source);
    await page.getByRole('button', { name: '发送', exact: true }).click();
    await expect(page.getByRole('list', { name: '历史消息' }).locator('.message')).toHaveCount(2, { timeout: 90000 });
    await expect(page.getByRole('alert')).not.toBeVisible();
    await page.getByRole('button', { name: '提炼当前对话', exact: true }).click();
    await expect(page.getByRole('status').filter({ hasText: '已提炼' })).toBeVisible({ timeout: 65000 });
    const extracted = await db.knowledgeEntry.findMany({ where: { kbId: kb.id, type: 'MANUAL' } });
    expect(extracted.length).toBeGreaterThan(0);
    for (const entry of extracted) {
      expect(entry.content.length).toBeGreaterThanOrEqual(200);
      await expect.poll(async () => (await db.knowledgeEntry.findUniqueOrThrow({ where: { id: entry.id } })).processingStatus, { timeout: 90000 }).toMatch(/^(COMPLETED|FAILED)$/);
      expect((await db.knowledgeEntry.findUniqueOrThrow({ where: { id: entry.id } })).processingStatus).toBe('COMPLETED');
      expect(await db.knowledgeChunk.count({ where: { entryId: entry.id } })).toBeGreaterThan(0);
    }
    await page.getByRole('link', { name: '数据统计', exact: true }).click();
    await expect(page.locator('.summary-grid dd')).toHaveText(['1', String(extracted.length + 1), '1']);
    await expect(page.getByRole('table', { name: '热门条目明细' }).getByRole('row').filter({ hasText: '全流程合成资料.txt' })).toBeVisible();
    await expect(page.locator('.analytics-chart svg')).toHaveCount(2);
    await page.screenshot({ path: testInfo.outputPath('workflow-analytics.png'), fullPage: true });
  } finally { await db.user.deleteMany({ where: { username } }); }
});
