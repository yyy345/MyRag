import { test, expect, db } from './fixtures';

test('POST SSE client reaches Express and reports a real pre-stream provider error', async ({ signedInPage: page, account }) => {
  test.skip(process.env.MIGRATION_EXTERNAL_AI === '1', 'Requires local-only provider adapters.');
  const kb = await db.knowledgeBase.create({ data: { userId: account.id, name: '流接口测试' } });
  const session = await db.chatSession.create({ data: { kbId: kb.id, userId: account.id, title: '新对话' } });
  const result = await page.evaluate(async ({ kb, sid }) => {
    // @ts-expect-error Vite serves the TypeScript module in this real browser test.
    const { streamChat } = await import('/src/api/sse.ts');
    try { for await (const _event of streamChat(kb, sid, '本地合成问题')) {} return 'unexpected success'; }
    catch (err) { return { message: (err as Error).message, status: (err as { status: number }).status }; }
  }, { kb: kb.id, sid: session.id });
  expect(result).toEqual({ message: '服务器内部错误', status: 500 });
  expect(await db.chatMessage.count({ where: { sessionId: session.id, role: 'USER' } })).toBe(1);
  expect(await db.chatMessage.count({ where: { sessionId: session.id, role: 'ASSISTANT' } })).toBe(0);
});
