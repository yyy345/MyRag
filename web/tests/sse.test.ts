import { describe, expect, it, vi } from 'vitest';
import { readSSE, createChatStream } from '../src/api/sse';

const encoder = new TextEncoder();
const frame = (data: unknown) => `data: ${JSON.stringify(data)}\n\n`;
const done = { type: 'done', is_end: true, references: [], usage: { prompt_tokens: 3, completion_tokens: 2 } };
function body(text: string, splitBytes = false) {
  const bytes = encoder.encode(text);
  return new ReadableStream<Uint8Array>({ start(controller) {
    if (splitBytes) for (const byte of bytes) controller.enqueue(new Uint8Array([byte]));
    else controller.enqueue(bytes);
    controller.close();
  } });
}
async function collect<T>(source: AsyncIterable<T>) { const result: T[] = []; for await (const value of source) result.push(value); return result; }

describe('POST SSE transport', () => {
  it('preserves Chinese and emoji across individual bytes, CRLF and multiline data', async () => {
    const content = { type: 'chunk', content: '中文🙂', is_end: false };
    const wire = `: heartbeat\r\nevent: message\r\ndata: {"type":"chunk",\r\ndata: "content":"中文🙂","is_end":false}\r\n\r\n${frame(done)}`;
    expect(await collect(readSSE(body(wire, true)))).toEqual([content, done]);
  });
  it('rejects broken, oversized, invalid UTF-8 and truncated streams', async () => {
    for (const wire of ['data: bad\n\n', frame({ type: 'done', is_end: false }), frame({ ...done, references: [null] }), frame({ type: 'chunk', content: 'partial', is_end: false }), `data: ${'a'.repeat(1024 * 1024 + 1)}\n\n`]) {
      await expect(collect(readSSE(body(wire)))).rejects.toThrow();
    }
    const invalid = new ReadableStream<Uint8Array>({ start(controller) { controller.enqueue(new Uint8Array([0xff])); controller.close(); } });
    await expect(collect(readSSE(invalid))).rejects.toThrow('UTF-8');
  });
  it('cancels and releases the reader on abort or consumer exit', async () => {
    const cancelled = vi.fn();
    const controller = new AbortController();
    const stream = new ReadableStream<Uint8Array>({ cancel: cancelled });
    const running = collect(readSSE(stream, controller.signal)); controller.abort();
    await expect(running).rejects.toMatchObject({ name: 'AbortError' });
    expect(cancelled).toHaveBeenCalledOnce(); expect(stream.locked).toBe(false);
    const early = new ReadableStream<Uint8Array>({ start(c) { c.enqueue(encoder.encode(frame({ type: 'chunk', content: 'one', is_end: false }))); }, cancel: cancelled });
    for await (const _ of readSSE(early)) break;
    expect(early.locked).toBe(false); expect(cancelled).toHaveBeenCalledTimes(2);
  });
  it('sends authenticated POST JSON and preserves reference/usage events', async () => {
    const transport = vi.fn<typeof fetch>().mockResolvedValue(new Response(body(frame(done)), { headers: { 'content-type': 'text/event-stream;charset=utf-8' } }));
    const request = createChatStream({ fetch: transport, token: () => 'test-token' });
    expect(await collect(request('kb', 'sid', '合成问题'))).toEqual([done]);
    expect(transport.mock.calls[0]![0]).toBe('/api/knowledge-bases/kb/chat/sessions/sid/messages');
    expect(transport.mock.calls[0]![1]).toMatchObject({ method: 'POST', body: JSON.stringify({ content: '合成问题' }), headers: { Authorization: 'Bearer test-token' } });
  });
  it('reports authentication, non-stream replies and idle timeout explicitly', async () => {
    const unauthorized = vi.fn();
    const transport = vi.fn<typeof fetch>().mockResolvedValueOnce(new Response(JSON.stringify({ code: 1001, message: '登录已失效' }), { status: 401 }))
      .mockResolvedValueOnce(new Response('{}', { headers: { 'content-type': 'application/json' } }))
      .mockResolvedValueOnce(new Response(new ReadableStream<Uint8Array>(), { headers: { 'content-type': 'text/event-stream' } }));
    const request = createChatStream({ fetch: transport, token: () => null, onUnauthorized: unauthorized, idleTimeoutMs: 20 });
    await expect(collect(request('kb', 'sid', 'q'))).rejects.toThrow('登录已失效');
    expect(unauthorized).toHaveBeenCalledOnce();
    await expect(collect(request('kb', 'sid', 'q'))).rejects.toThrow('未返回回答流');
    await expect(collect(request('kb', 'sid', 'q'))).rejects.toThrow('超时');
  });
});
