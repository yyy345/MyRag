import type { SSEChunk } from 'shared';
import { ApiError } from './client';
import { sessionsPath } from './chat';

function parseEvent(data: string): SSEChunk {
  let event: SSEChunk;
  try { event = JSON.parse(data); } catch { throw new ApiError('回答流包含无效数据'); }
  if (!event || (event.type !== 'chunk' && event.type !== 'done') || event.is_end !== (event.type === 'done') ||
    (event.type === 'chunk' && typeof event.content !== 'string') ||
    (event.content !== undefined && typeof event.content !== 'string') ||
    (event.references != null && (!Array.isArray(event.references) || event.references.some(ref => !ref || typeof ref.id !== 'string' || typeof ref.title !== 'string' || typeof ref.content !== 'string'))) ||
    (event.usage != null && (![event.usage.prompt_tokens, event.usage.completion_tokens].every(n => typeof n === 'number' && Number.isFinite(n) && n >= 0)))) {
    throw new ApiError('回答流格式不正确');
  }
  return event;
}

/** Parse SSE lines, independently of network chunks and UTF-8 character boundaries. */
export async function* readSSE(body: ReadableStream<Uint8Array>, signal?: AbortSignal): AsyncGenerator<SSEChunk> {
  const reader = body.getReader(); const decoder = new TextDecoder('utf-8', { fatal: true });
  let buffer = ''; let data: string[] = []; let eventSize = 0;
  const abort = () => { void reader.cancel().catch(() => {}); };
  signal?.addEventListener('abort', abort, { once: true });
  try {
    while (true) {
      if (signal?.aborted) throw new DOMException('已停止生成', 'AbortError');
      const result = await reader.read();
      if (signal?.aborted) throw new DOMException('已停止生成', 'AbortError');
      try { buffer += decoder.decode(result.value, { stream: !result.done }); }
      catch { throw new ApiError('回答流包含无效 UTF-8 文本'); }
      let match: RegExpExecArray | null;
      while ((match = /\r\n|\r|\n/.exec(buffer))) {
        if (!result.done && match[0] === '\r' && match.index === buffer.length - 1) break;
        const line = buffer.slice(0, match.index); buffer = buffer.slice(match.index + match[0].length);
        if (!line) {
          if (!data.length) continue;
          const event = parseEvent(data.join('\n')); data = []; eventSize = 0;
          yield event;
          if (event.type === 'done') return;
        } else if (line === 'data' || line.startsWith('data:')) {
          const value = line === 'data' ? '' : line.slice(5).replace(/^ /, '');
          eventSize += value.length;
          if (eventSize > 1024 * 1024) throw new ApiError('单条回答流数据过大');
          data.push(value);
        }
      }
      if (buffer.length > 1024 * 1024) throw new ApiError('单条回答流数据过大');
      if (result.done) throw new ApiError('回答连接意外中断，请重试');
    }
  } finally {
    signal?.removeEventListener('abort', abort);
    await reader.cancel().catch(() => {}); reader.releaseLock();
  }
}

export function createChatStream(options: { fetch?: typeof fetch; baseUrl?: string; token: () => string | null; onUnauthorized?: () => void; idleTimeoutMs?: number }) {
  return async function* (kb: string, sid: string, content: string, signal?: AbortSignal): AsyncGenerator<SSEChunk> {
    const controller = new AbortController(); let timedOut = false;
    const abort = () => controller.abort();
    if (signal?.aborted) abort(); else signal?.addEventListener('abort', abort, { once: true });
    let timer: ReturnType<typeof setTimeout>;
    const resetTimer = () => { clearTimeout(timer); timer = setTimeout(() => { timedOut = true; controller.abort(); }, options.idleTimeoutMs ?? 60000); };
    resetTimer();
    try {
      const token = options.token();
      const response = await (options.fetch ?? fetch)(`${(options.baseUrl ?? '/api').replace(/\/$/, '')}${sessionsPath(kb)}/${encodeURIComponent(sid)}/messages`, {
        method: 'POST', headers: { 'Content-Type': 'application/json', Accept: 'text/event-stream', ...(token ? { Authorization: `Bearer ${token}` } : {}) },
        body: JSON.stringify({ content }), signal: controller.signal,
      });
      if (response.status === 401) options.onUnauthorized?.();
      if (!response.ok) {
        const result = await response.json().catch(() => null);
        throw new ApiError(result?.message || `请求失败（${response.status}）`, response.status, result?.code);
      }
      if (!response.headers.get('content-type')?.toLowerCase().includes('text/event-stream') || !response.body) throw new ApiError('服务器未返回回答流');
      for await (const event of readSSE(response.body, controller.signal)) { resetTimer(); yield event; }
    } catch (err) {
      if (signal?.aborted) throw new DOMException('已停止生成', 'AbortError');
      if (timedOut) throw new ApiError('回答等待超时，请重试');
      if (err instanceof ApiError) throw err;
      throw new ApiError('回答连接失败，请重试');
    } finally { clearTimeout(timer!); signal?.removeEventListener('abort', abort); controller.abort(); }
  };
}

export const streamChat = createChatStream({
  baseUrl: import.meta.env.VITE_API_BASE_URL || '/api',
  token: () => localStorage.getItem('token'),
  onUnauthorized: () => { localStorage.removeItem('token'); localStorage.removeItem('user'); window.dispatchEvent(new Event('auth:unauthorized')); },
});
