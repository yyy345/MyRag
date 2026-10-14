import { describe, expect, it, vi } from 'vitest';
import { createApiClient, ApiError } from '../src/api/client';

const reply = (status: number, body: unknown) => new Response(JSON.stringify(body), { status });
describe('API compatibility and failure handling', () => {
  it('preserves pagination, attaches credentials and omits empty query values', async () => {
    const data = { total: 0, records: [], current: 1, size: 10 };
    const transport = vi.fn<typeof fetch>().mockResolvedValue(reply(200, { code: 0, data, message: 'ok' }));
    const request = createApiClient({ fetch: transport, token: () => 'jwt' });
    expect(await request('/knowledge-bases', { query: { page: 1, search: undefined } })).toEqual(data);
    expect(transport.mock.calls[0]![0]).toBe('/api/knowledge-bases?page=1');
    expect(new Headers(transport.mock.calls[0]![1]?.headers).get('Authorization')).toBe('Bearer jwt');
  });
  it('reads captcha JSON regardless of legacy content type', async () => {
    const request = createApiClient({ fetch: vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify({ code: 0, data: { id: 'id', svg: '<svg/>' } }), { headers: { 'content-type': 'image/svg+xml' } })) });
    expect(await request('/auth/captcha')).toEqual({ id: 'id', svg: '<svg/>' });
  });
  it('expires protected sessions once per response, but preserves login errors', async () => {
    const onUnauthorized = vi.fn();
    const request = createApiClient({ onUnauthorized, fetch: vi.fn<typeof fetch>().mockImplementation(async () => reply(401, { code: 1002, message: '用户名或密码错误', data: null })) });
    await expect(request('/auth/login', { method: 'POST', body: {} })).rejects.toThrow('用户名或密码错误');
    expect(onUnauthorized).not.toHaveBeenCalled();
    await expect(request('/auth/me')).rejects.toMatchObject({ status: 401, code: 1002 });
    expect(onUnauthorized).toHaveBeenCalledTimes(1);
  });
  it('rejects application errors, invalid JSON and transport failures', async () => {
    const transport = vi.fn<typeof fetch>().mockResolvedValueOnce(reply(200, { code: 2003, message: '解析失败', data: null }))
      .mockResolvedValueOnce(new Response('bad json')).mockRejectedValueOnce(new TypeError('failed'));
    const request = createApiClient({ fetch: transport });
    await expect(request('/test')).rejects.toThrow('解析失败');
    await expect(request('/test')).rejects.toThrow('服务器返回了无效数据');
    await expect(request('/test')).rejects.toThrow('网络连接失败');
  });
  it('lets fetch set multipart boundaries and accepts null data', async () => {
    const transport = vi.fn<typeof fetch>().mockResolvedValue(reply(200, { code: 0, data: null }));
    const form = new FormData(); form.append('file', new Blob(['text']), 'test.txt');
    const request = createApiClient({ fetch: transport });
    expect(await request('/upload', { method: 'POST', body: form })).toBeNull();
    expect(new Headers(transport.mock.calls[0]![1]?.headers).has('Content-Type')).toBe(false);
    expect(transport.mock.calls[0]![1]?.body).toBe(form);
  });
  it('aborts slow requests and reports an actionable timeout', async () => {
    const transport: typeof fetch = (_url, init) => new Promise((_resolve, reject) => { init!.signal!.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError'))); });
    const request = createApiClient({ fetch: transport, timeoutMs: 5 });
    await expect(request('/slow')).rejects.toEqual(new ApiError('请求超时，请稍后重试'));
  });
});
