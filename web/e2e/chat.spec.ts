import { test, expect, db } from './fixtures';

test('sessions create, switch, restore ordered history and delete with database verification', async ({ signedInPage: page, account }) => {
  const kb = await db.knowledgeBase.create({ data: { userId: account.id, name: '会话验收' } });
  const history = await db.chatSession.create({ data: { kbId: kb.id, userId: account.id, title: '历史会话' } });
  await db.chatMessage.createMany({ data: [
    { sessionId: history.id, role: 'ASSISTANT', content: '第二条回答 **重点**', createdAt: new Date('2026-01-01T00:00:01Z') },
    { sessionId: history.id, role: 'USER', content: '第一条问题', createdAt: new Date('2026-01-01T00:00:00Z') },
  ] });
  await page.goto(`/knowledge-bases/${kb.id}/chat/${history.id}`);
  await expect(page.getByRole('list', { name: '历史消息' }).locator('li')).toHaveCount(2);
  await expect(page.getByRole('list', { name: '历史消息' }).locator('li').first()).toContainText('第一条问题');
  await page.reload();
  await expect(page.locator('.message strong')).toHaveText('重点');
  await page.getByRole('button', { name: '新对话', exact: true }).click();
  await expect(page.getByText('当前对话暂无消息。')).toBeVisible();
  const created = await db.chatSession.findFirstOrThrow({ where: { kbId: kb.id, id: { not: history.id } } });
  await expect(page).toHaveURL(new RegExp(`${created.id}$`));
  await page.getByRole('link', { name: '历史会话', exact: true }).click();
  await expect(page.getByText('第一条问题', { exact: true })).toBeVisible();
  await page.getByRole('button', { name: '删除对话 历史会话', exact: true }).click();
  await page.getByRole('button', { name: '取消', exact: true }).click();
  expect(await db.chatMessage.count({ where: { sessionId: history.id } })).toBe(2);
  await page.getByRole('button', { name: '删除对话 历史会话', exact: true }).click();
  await page.getByRole('button', { name: '确认删除', exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`/knowledge-bases/${kb.id}/chat$`));
  await expect(page.getByText('选择一个对话，或创建新对话。')).toBeVisible();
  expect(await db.chatMessage.count({ where: { sessionId: history.id } })).toBe(0);
  expect(await db.chatSession.findUnique({ where: { id: history.id } })).toBeNull();
  await page.getByRole('link', { name: '新对话', exact: true }).click();
  await expect(page.getByText('当前对话暂无消息。')).toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});

test('history failures retry and stale requests cannot overwrite another session', async ({ signedInPage: page, account }) => {
  const kb = await db.knowledgeBase.create({ data: { userId: account.id, name: '会话切换测试' } });
  const slow = await db.chatSession.create({ data: { kbId: kb.id, userId: account.id, title: '旧会话' } });
  const fast = await db.chatSession.create({ data: { kbId: kb.id, userId: account.id, title: '新会话' } });
  await db.chatMessage.createMany({ data: [
    { sessionId: slow.id, role: 'USER', content: '旧会话内容' },
    { sessionId: fast.id, role: 'USER', content: '新会话内容' },
  ] });
  await page.route(`**/chat/sessions/${fast.id}`, route => route.abort('failed'), { times: 1 });
  await page.goto(`/knowledge-bases/${kb.id}/chat/${fast.id}`);
  await expect(page.getByRole('alert')).toContainText('网络连接失败');
  await page.getByRole('button', { name: '重试', exact: true }).click();
  await expect(page.getByText('新会话内容', { exact: true })).toBeVisible();
  let release!: () => void; let requested!: () => void;
  const held = new Promise<void>(resolve => { release = resolve; });
  const arrived = new Promise<void>(resolve => { requested = resolve; });
  await page.route(`**/chat/sessions/${slow.id}`, async route => {
    const response = await route.fetch(); requested(); await held;
    await route.fulfill({ response }).catch(() => {}); // Aborting the old read is expected.
  });
  try {
    await page.getByRole('link', { name: '旧会话', exact: true }).click();
    await arrived;
    await page.getByRole('link', { name: '新会话', exact: true }).click();
    await expect(page.getByText('新会话内容', { exact: true })).toBeVisible();
  } finally { release(); }
  await expect(page.getByText('旧会话内容', { exact: true })).not.toBeVisible();
  await page.goto(`/knowledge-bases/${kb.id}/chat/unknown`);
  await expect(page.getByRole('alert')).toContainText('对话不存在或不属于当前知识库');
  await expect(page.getByRole('list', { name: '历史消息' }).locator('li')).toHaveCount(0);
});

test('chat follows the latest history and lets the user scroll back without forcing the bottom', async ({ signedInPage: page, account }) => {
  const kb = await db.knowledgeBase.create({ data: { userId: account.id, name: '滚动验收' } });
  const session = await db.chatSession.create({ data: { kbId: kb.id, userId: account.id, title: '长历史' } });
  await db.chatMessage.createMany({ data: Array.from({ length: 24 }, (_, index) => ({ sessionId: session.id, role: 'USER' as const, content: `合成消息 ${index + 1}：${'用于检查历史滚动。'.repeat(10)}`, createdAt: new Date(2026, 0, 1, 0, 0, index) })) });
  await page.goto(`/knowledge-bases/${kb.id}/chat/${session.id}`);
  const panel = page.locator('.chat-history');
  await expect.poll(() => panel.evaluate(el => el.scrollHeight - el.clientHeight - el.scrollTop)).toBeLessThan(5);
  await panel.evaluate(el => { el.scrollTop = 0; el.dispatchEvent(new Event('scroll')); });
  await expect(page.getByRole('button', { name: '回到最新消息', exact: true })).toBeVisible();
  await page.getByLabel('问题', { exact: true }).fill('编辑问题不应改变阅读位置');
  expect(await panel.evaluate(el => el.scrollTop)).toBe(0);
  await page.getByRole('button', { name: '回到最新消息', exact: true }).click();
  await expect.poll(() => panel.evaluate(el => el.scrollHeight - el.clientHeight - el.scrollTop)).toBeLessThan(5);
});
