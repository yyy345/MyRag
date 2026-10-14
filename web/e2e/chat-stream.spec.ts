import { test, expect, db } from './fixtures';

test('chat displays references safely and reports local generation failure without a successful answer', async ({ signedInPage: page, account }) => {
  test.skip(process.env.MIGRATION_EXTERNAL_AI === '1', 'Local-only failure fixture.');
  const kb = await db.knowledgeBase.create({ data: { userId: account.id, name: '消息展示测试' } });
  const session = await db.chatSession.create({ data: { kbId: kb.id, userId: account.id, title: '引用会话' } });
  await db.chatMessage.create({ data: { sessionId: session.id, role: 'ASSISTANT', content: '**历史回答** <script>window.pwned=1</script>', references: [{ id: 'ref', title: '合成引用', content: '[危险链接](javascript:alert(1))' }], tokenUsage: { prompt_tokens: 12, completion_tokens: 8 } } });
  await page.goto(`/knowledge-bases/${kb.id}/chat/${session.id}`);
  await expect(page.getByText('历史回答', { exact: true })).toBeVisible();
  await page.getByText('引用资料（1）', { exact: true }).click();
  await expect(page.getByRole('heading', { name: '合成引用' })).toBeVisible();
  await expect(page.getByText('Token 用量（估算）：输入 12 · 输出 8')).toBeVisible();
  expect(await page.locator('.message script, .references a[href^="javascript:"]').count()).toBe(0);
  await page.getByLabel('问题', { exact: true }).fill('本地合成问题');
  await page.getByRole('button', { name: '发送', exact: true }).click();
  await expect(page.getByRole('alert')).toContainText('本次回答未完成');
  await expect(page.getByLabel('问题', { exact: true })).toBeEnabled();
  expect(await db.chatMessage.count({ where: { sessionId: session.id, role: 'ASSISTANT' } })).toBe(1);
});

test('stopping or switching sessions aborts a pending POST and clears its temporary messages', async ({ signedInPage: page, account }) => {
  const kb = await db.knowledgeBase.create({ data: { userId: account.id, name: '取消测试' } });
  const other = await db.chatSession.create({ data: { kbId: kb.id, userId: account.id, title: '其他会话' } });
  let release!: () => void; const held = new Promise<void>(resolve => { release = resolve; });
  const pending: Promise<unknown>[] = [];
  await page.route('**/chat/sessions/*/messages', async route => {
    const reply = held.then(() => route.fulfill({ contentType: 'text/event-stream', body: 'data: {"type":"chunk","content":"迟到回答","is_end":false}\n\ndata: {"type":"done","is_end":true}\n\n' })).catch(() => {});
    pending.push(reply); await reply;
  });
  try {
    await page.goto(`/knowledge-bases/${kb.id}/chat`);
    await page.getByLabel('问题', { exact: true }).fill('自动建会话');
    await page.getByRole('button', { name: '发送', exact: true }).click();
    await expect(page.getByRole('button', { name: '停止生成', exact: true })).toBeVisible();
    expect(await db.chatSession.count({ where: { kbId: kb.id } })).toBe(2);
    await page.getByRole('button', { name: '停止生成', exact: true }).click();
    await expect(page.getByText('已停止生成，当前回答尚未完成。')).toBeVisible();
    await page.getByLabel('问题', { exact: true }).fill('切换时取消');
    await page.getByRole('button', { name: '发送', exact: true }).click();
    await expect(page.getByRole('button', { name: '停止生成', exact: true })).toBeVisible();
    await page.getByRole('link', { name: '其他会话', exact: true }).click();
    await expect(page).toHaveURL(new RegExp(`${other.id}$`));
    await expect(page.getByText('当前对话暂无消息。')).toBeVisible();
  } finally { release(); await Promise.all(pending); }
  await expect(page.getByText('迟到回答', { exact: true })).not.toBeVisible();
  await expect(page.getByText('切换时取消', { exact: true })).not.toBeVisible();
  await expect(page.getByRole('button', { name: '停止生成', exact: true })).not.toBeVisible();
});

test('external integration completes a real streamed answer with persisted references and usage', async ({ signedInPage: page, account }, testInfo) => {
  test.skip(process.env.MIGRATION_EXTERNAL_AI !== '1', 'Requires the configured external model.');
  test.setTimeout(150000);
  const text = '这是独立测试环境的合成资料。知识库支持文档上传和问答。';
  const kb = await db.knowledgeBase.create({ data: { userId: account.id, name: '真实问答验收' } });
  const upload = await page.request.post(`/api/knowledge-bases/${kb.id}/entries/upload`, { headers: { Authorization: `Bearer ${account.token}` }, multipart: { file: { name: '合成资料.txt', mimeType: 'text/plain', buffer: Buffer.from(text) } } });
  expect(upload.ok()).toBe(true);
  await expect.poll(async () => (await db.knowledgeEntry.findFirstOrThrow({ where: { kbId: kb.id } })).processingStatus, { timeout: 90000 }).toMatch(/^(COMPLETED|FAILED)$/);
  const entry = await db.knowledgeEntry.findFirstOrThrow({ where: { kbId: kb.id } });
  expect(entry.processingStatus).toBe('COMPLETED');
  await page.goto(`/knowledge-bases/${kb.id}/chat`);
  await page.getByLabel('问题', { exact: true }).fill(text);
  await page.getByRole('button', { name: '发送', exact: true }).click();
  await expect(page.getByRole('list', { name: '历史消息' }).locator('.message')).toHaveCount(2, { timeout: 90000 });
  await expect(page.getByRole('alert')).not.toBeVisible();
  await expect(page.locator('.token-usage')).toBeVisible();
  const reply = await db.chatMessage.findFirstOrThrow({ where: { session: { kbId: kb.id }, role: 'ASSISTANT' } });
  expect(reply.content.trim().length).toBeGreaterThan(0);
  expect(reply.references).toEqual(expect.arrayContaining([expect.objectContaining({ id: entry.id })]));
  expect(reply.tokenUsage).toEqual(expect.objectContaining({ prompt_tokens: expect.any(Number), completion_tokens: expect.any(Number) }));
  await page.getByText('引用资料（1）', { exact: true }).click();
  await expect(page.getByRole('heading', { name: '合成资料.txt' })).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath('chat-answer.png'), fullPage: true });
  await page.reload();
  await expect(page.getByRole('list', { name: '历史消息' }).locator('.message')).toHaveCount(2);
});
