import { test, expect, db } from './fixtures';

test('extraction handles no history, provider failure and a successful API result', async ({ signedInPage: page, account }) => {
  test.skip(process.env.MIGRATION_EXTERNAL_AI === '1', 'Local-only provider error verification.');
  const kb = await db.knowledgeBase.create({ data: { userId: account.id, name: '提炼验收库' } });
  await page.goto(`/knowledge-bases/${kb.id}/chat`);
  await page.getByRole('button', { name: '提炼全部对话', exact: true }).click();
  await expect(page.getByRole('status')).toHaveText('未提炼到新知识');
  const response = await page.request.post(`/api/knowledge-bases/${kb.id}/chat/sessions`, { headers: { Authorization: `Bearer ${account.token}` } });
  expect(response.ok()).toBe(true);
  const { data: session } = await response.json();
  await db.chatMessage.create({ data: { sessionId: session.id, role: 'USER', content: '本地合成消息' } });
  await page.goto(`/knowledge-bases/${kb.id}/chat/${session.id}`);
  await page.getByRole('button', { name: '提炼当前对话', exact: true }).click();
  await expect(page.getByRole('alert')).toHaveText('服务器内部错误');
  // Verify the success UI contract separately; this is not a real model assertion.
  await page.route(`**/knowledge-bases/${kb.id}/chat/extract`, async route => {
    expect(route.request().postDataJSON()).toEqual({ sessionId: session.id });
    await route.fulfill({ json: { code: 0, data: { entries: [{ id: 'fixture', title: '合成知识' }], count: 1 }, message: 'ok' } });
  });
  await page.getByRole('button', { name: '提炼当前对话', exact: true }).click();
  await expect(page.getByRole('status')).toContainText('已提炼 1 条知识');
  await expect(page.getByRole('alert')).not.toBeVisible();
});
