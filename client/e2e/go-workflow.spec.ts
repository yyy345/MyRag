import { randomUUID } from 'node:crypto';
import { test, expect, db, readCaptcha } from '../../web/e2e/fixtures';

for (const failure of [
  { name: 'HTTP service error', status: 503, contentType: 'application/json', body: JSON.stringify({ code: 5000, message: '回答生成暂不可用，请重试' }), message: '回答生成暂不可用，请重试' },
  { name: 'terminal error after partial answer', status: 200, contentType: 'text/event-stream', body: 'data: {"type":"chunk","content":"未完成的回答","is_end":false}\n\ndata: {"type":"done","content":"回答生成暂不可用，请重试","is_end":true}\n\n', message: '回答生成暂不可用，请重试' },
  { name: 'connection ends without completion', status: 200, contentType: 'text/event-stream', body: 'data: {"type":"chunk","content":"未完成的回答","is_end":false}\n\n', message: '回答连接中断，请重新发送' },
  { name: 'invalid stream JSON', status: 200, contentType: 'text/event-stream', body: 'data: {invalid}\n\n', message: '回答数据格式异常，请重新发送' },
]) {
  test(`React displays ${failure.name} and allows retry`, async ({ signedInPage: page, account }) => {
    const kb = await db.knowledgeBase.create({ data: { userId: account.id, name: '问答错误提示验收' } });
    const session = await db.chatSession.create({ data: { kbId: kb.id, userId: account.id, title: '错误处理验收' } });
    await page.route(`**/api/knowledge-bases/${kb.id}/chat/sessions/${session.id}/messages`, async route => {
      if (route.request().method() === 'POST') await route.fulfill(failure);
      else await route.continue();
    });
    await page.goto(`/knowledge-bases/${kb.id}/chat/${session.id}`);
    const input = page.getByPlaceholder('输入问题，Enter 发送，Shift+Enter 换行...');
    await expect(input).toBeEnabled();
    await input.fill('请回答Vue的问题');
    await input.press('Enter');
    await expect(page.locator('.ant-message-error')).toHaveText(failure.message);
    await expect(input).toBeEnabled();
    await expect(page.getByText('未完成的回答', { exact: true })).not.toBeVisible();
    expect(await db.chatMessage.count({ where: { sessionId: session.id, role: 'ASSISTANT' } })).toBe(0);
  });
}

test('original React pages complete registration, upload, Go/Python chat, extraction and analytics', async ({ page }, testInfo) => {
  const username = `react_flow_${randomUUID().slice(0, 8)}`;
  try {
    const captcha = await readCaptcha(page, () => page.goto('/register'));
    await page.getByPlaceholder('用户名', { exact: true }).fill(username);
    await page.getByPlaceholder('邮箱', { exact: true }).fill(`${username}@example.test`);
    await page.getByPlaceholder('密码', { exact: true }).fill('Workflow-local-2026!');
    await page.getByPlaceholder('验证码', { exact: true }).fill(captcha.answer);
    await page.getByRole('button', { name: /注\s*册/ }).click();
    await expect(page.getByRole('heading', { name: '我的知识库', exact: true })).toBeVisible();
    await page.getByRole('button', { name: /新建知识库/ }).click();
    await page.getByLabel('名称', { exact: true }).fill('React Go Python 验收库');
    await page.getByLabel('AI 人设提示词', { exact: true }).fill('请依据资料回答并保留引用。');
    await page.getByRole('dialog').getByRole('button', { name: /创\s*建/ }).click();
    await expect(page.getByRole('dialog')).not.toBeVisible();
    const kb = await db.knowledgeBase.findFirstOrThrow({ where: { user: { username } } });
    await page.getByText('React Go Python 验收库', { exact: true }).click();
    await page.getByRole('button', { name: /上传文档/ }).click();
    await page.locator('input[type="file"]').first().setInputFiles({ name: '合成资料.txt', mimeType: 'text/plain', buffer: Buffer.from('合成资料介绍知识库上传、检索和流式问答。🙂') });
    await expect(page.getByRole('dialog')).not.toBeVisible();
    await expect.poll(async () => (await db.knowledgeEntry.findFirstOrThrow({ where: { kbId: kb.id } })).processingStatus, { timeout: 20000 }).toBe('COMPLETED');
    await expect(page.getByRole('row').filter({ hasText: '合成资料.txt' })).toContainText('已完成');
    await page.getByRole('button', { name: /对话问答/ }).click();
    await page.getByRole('button', { name: /新对话/ }).click();
    await expect(page).toHaveURL(/\/chat\/[^/]+$/);
    await page.getByPlaceholder('输入问题，Enter 发送，Shift+Enter 换行...').fill('知识库如何问答？');
    await page.getByPlaceholder('输入问题，Enter 发送，Shift+Enter 换行...').press('Enter');
    await expect(page.getByText('中文🙂回答', { exact: true })).toBeVisible();
    await expect(page.getByText('12 prompt + 4 completion tokens', { exact: true })).toBeVisible();
    const session = await db.chatSession.findFirstOrThrow({ where: { kbId: kb.id } });
    const answer = await db.chatMessage.findFirstOrThrow({ where: { sessionId: session.id, role: 'ASSISTANT' } });
    expect(answer.references).toHaveLength(1);
    await page.reload();
    await expect(page.getByText('中文🙂回答', { exact: true })).toBeVisible();
    await page.getByRole('button', { name: /提炼知识/ }).click();
    await expect(page.getByText('成功提炼 1 条新知识', { exact: true })).toBeVisible();
    const extracted = await db.knowledgeEntry.findFirstOrThrow({ where: { kbId: kb.id, type: 'MANUAL' } });
    await expect.poll(async () => (await db.knowledgeEntry.findUniqueOrThrow({ where: { id: extracted.id } })).processingStatus, { timeout: 20000 }).toBe('COMPLETED');
    await page.getByRole('button', { name: /提炼知识/ }).click();
    await expect(page.getByText('未提炼到新知识', { exact: true })).toBeVisible();
    await page.getByRole('menuitem', { name: /数据统计/ }).click();
    await expect(page.locator('.ant-statistic-content-value')).toHaveText(['1', '2', '1']);
    await expect(page.getByRole('row').filter({ hasText: 'React Go Python 验收库' })).toBeVisible();
    await expect(page.locator('.echarts-for-react canvas')).toHaveCount(2);
    await page.screenshot({ path: testInfo.outputPath('react-go-python-analytics.png'), fullPage: true });
    await page.locator('.ant-dropdown-trigger').hover();
    await page.getByRole('menuitem', { name: /退出登录/ }).click();
    await expect(page.getByRole('heading', { name: 'AI 知识库平台', exact: true })).toBeVisible();
    await expect(page.getByTitle('点击刷新验证码', { exact: true })).toHaveCSS('opacity', '1');
    const loginCaptcha = await readCaptcha(page, () => page.getByTitle('点击刷新验证码', { exact: true }).click());
    await page.getByPlaceholder('用户名 / 邮箱', { exact: true }).fill(username);
    await page.getByPlaceholder('密码', { exact: true }).fill('Workflow-local-2026!');
    await page.getByPlaceholder('验证码', { exact: true }).fill(loginCaptcha.answer);
    await page.getByRole('button', { name: /登\s*录/ }).click();
    await expect(page.getByRole('heading', { name: '我的知识库', exact: true })).toBeVisible();
  } finally { await db.user.deleteMany({ where: { username } }); }
});

test('React manual entry forms, preview, edit and delete work against Go', async ({ signedInPage: page, account }) => {
  const kb = await db.knowledgeBase.create({ data: { userId: account.id, name: 'React条目管理' } });
  await page.goto(`/knowledge-bases/${kb.id}`);
  await page.getByRole('button', { name: /手动录入/ }).click();
  await page.getByLabel('标题', { exact: true }).fill('手动知识');
  await page.getByPlaceholder('使用 Markdown 格式编写知识内容...').fill('# React原页面\n\n手动知识正文🙂');
  await page.getByRole('dialog').getByRole('button', { name: /创\s*建/ }).click();
  await expect(page.getByRole('dialog')).not.toBeVisible();
  const entry = await db.knowledgeEntry.findFirstOrThrow({ where: { kbId: kb.id } });
  expect(entry.content).toContain('手动知识正文🙂');
  const row = page.getByRole('row').filter({ hasText: '手动知识' });
  await row.getByRole('button', { name: /预\s*览/ }).click();
  await expect(page.getByRole('dialog').getByRole('heading', { name: 'React原页面' })).toBeVisible();
  await page.getByRole('dialog').getByRole('button', { name: 'Close', exact: true }).click();
  await row.getByRole('button', { name: /编\s*辑/ }).click();
  await page.getByLabel('标题', { exact: true }).fill('已编辑知识');
  await page.getByRole('dialog').getByRole('button', { name: /保\s*存/ }).click();
  await expect(page.getByRole('row').filter({ hasText: '已编辑知识' })).toBeVisible();
  const headers = { Authorization: `Bearer ${account.token}` };
  expect((await page.request.get(`/api/knowledge-bases/${kb.id}/entries/${entry.id}`)).status()).toBe(401);
  expect((await page.request.get(`/api/knowledge-bases/${randomUUID()}/entries/${entry.id}`, { headers })).status()).toBe(404);
  await page.getByRole('row').filter({ hasText: '已编辑知识' }).getByRole('button', { name: /删\s*除/ }).click();
  await page.getByRole('dialog').getByRole('button', { name: /确\s*认/ }).click();
  await expect(page.getByRole('row').filter({ hasText: '已编辑知识' })).not.toBeVisible();
  expect(await db.knowledgeEntry.findUnique({ where: { id: entry.id } })).toBeNull();
});
