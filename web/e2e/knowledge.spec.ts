import { test, expect, db } from './fixtures';

test('knowledge library create, edit, cancel and delete persist correctly', async ({ signedInPage: page, account }) => {
  await expect(page.getByText('还没有知识库。', { exact: false })).toBeVisible();
  await page.getByRole('button', { name: '新建知识库' }).click();
  await page.getByLabel('名称', { exact: true }).fill('迁移验证资料库');
  await page.getByLabel('描述', { exact: true }).fill('用于验证真实 CRUD');
  await page.getByLabel('回答要求（可选）').fill('请引用来源');
  await page.getByRole('button', { name: '保存', exact: true }).click();
  const card = page.getByRole('article', { name: '迁移验证资料库', exact: true });
  await expect(card).toBeVisible();
  await page.screenshot({ path: test.info().outputPath('knowledge-library.png'), fullPage: true });
  const kb = await db.knowledgeBase.findFirstOrThrow({ where: { userId: account.id } });
  expect(kb.systemPrompt).toBe('请引用来源');
  await card.getByRole('button', { name: '编辑' }).click();
  await page.getByLabel('名称', { exact: true }).fill('更新后的资料库');
  await page.getByRole('button', { name: '保存', exact: true }).click();
  const edited = page.getByRole('article', { name: '更新后的资料库', exact: true });
  await expect(edited).toBeVisible();
  expect((await db.knowledgeBase.findUniqueOrThrow({ where: { id: kb.id } })).name).toBe('更新后的资料库');
  await edited.getByRole('button', { name: '删除', exact: true }).click();
  await page.getByRole('button', { name: '取消', exact: true }).click();
  await expect(edited).toBeVisible();
  await edited.getByRole('button', { name: '删除', exact: true }).click();
  await page.getByRole('button', { name: '确认删除' }).click();
  await expect(edited).toHaveCount(0);
  expect(await db.knowledgeBase.findUnique({ where: { id: kb.id } })).toBeNull();
  await expect(page.getByText('还没有知识库。', { exact: false })).toBeVisible();
});

test('pagination follows the real total and network failures offer retry', async ({ signedInPage: page, account }) => {
  for (let index = 0; index < 11; index++) {
    const response = await page.request.post('http://127.0.0.1:3100/api/knowledge-bases', { headers: { Authorization: `Bearer ${account.token}` }, data: { name: `分页资料 ${index}` } });
    expect(response.status()).toBe(200);
  }
  await page.reload();
  await expect(page.getByRole('article')).toHaveCount(10);
  await page.getByRole('button', { name: '下一页' }).click();
  await expect(page.getByRole('article')).toHaveCount(1);
  await page.route('**/api/knowledge-bases?*', route => route.abort());
  await page.getByRole('button', { name: '上一页' }).click();
  await expect(page.getByRole('alert')).toContainText('网络连接失败');
  await page.unroute('**/api/knowledge-bases?*');
  await page.getByRole('button', { name: '重试', exact: true }).click();
  await expect(page.getByRole('article')).toHaveCount(10);
  await expect(page.getByRole('alert')).toHaveCount(0);
});
