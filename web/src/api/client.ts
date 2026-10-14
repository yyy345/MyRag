import type { ApiResponse } from 'shared';

export class ApiError extends Error {
  constructor(message: string, public readonly status = 0, public readonly code?: number) { super(message); this.name = 'ApiError'; }
}
interface ClientOptions {
  baseUrl?: string;
  fetch?: typeof fetch;
  token?: () => string | null;
  onUnauthorized?: () => void;
  timeoutMs?: number;
}
interface RequestOptions { method?: string; body?: unknown; query?: Record<string, unknown>; signal?: AbortSignal }

export function createApiClient(options: ClientOptions = {}) {
  const base = (options.baseUrl ?? '/api').replace(/\/$/, '');
  return async function request<T>(path: string, requestOptions: RequestOptions = {}): Promise<T> {
    if (!path.startsWith('/') || path.startsWith('//')) throw new ApiError('无效的接口路径');
    const params = new URLSearchParams();
    for (const [key, value] of Object.entries(requestOptions.query || {})) {
      if (value !== undefined && value !== null && value !== '') params.set(key, String(value));
    }
    const url = `${base}${path}${params.size ? `?${params}` : ''}`;
    const headers = new Headers({ Accept: 'application/json' });
    const isAuth = ['/auth/login', '/auth/register', '/auth/captcha'].includes(path);
    const token = options.token?.();
    if (token && !isAuth) headers.set('Authorization', `Bearer ${token}`);
    const body = requestOptions.body;
    const isForm = body instanceof FormData;
    if (body !== undefined && !isForm) headers.set('Content-Type', 'application/json');
    const controller = new AbortController();
    const abort = () => controller.abort(requestOptions.signal?.reason);
    if (requestOptions.signal?.aborted) abort();
    else requestOptions.signal?.addEventListener('abort', abort, { once: true });
    let timedOut = false;
    const timer = setTimeout(() => { timedOut = true; controller.abort(); }, options.timeoutMs ?? 60000);
    try {
      const response = await (options.fetch ?? fetch)(url, {
        method: requestOptions.method || 'GET', headers,
        body: body === undefined ? undefined : isForm ? body : JSON.stringify(body), signal: controller.signal,
      });
      if (response.status === 401 && !isAuth) options.onUnauthorized?.();
      // Legacy captcha returns JSON with an SVG Content-Type, so parse the body explicitly.
      let result: ApiResponse<T>;
      try { result = await response.json(); }
      catch { throw new ApiError(response.ok ? '服务器返回了无效数据' : `请求失败（${response.status}）`, response.status); }
      if (!response.ok || result.code !== 0) throw new ApiError(result.message || `请求失败（${response.status}）`, response.status, result.code);
      if (!Object.prototype.hasOwnProperty.call(result, 'data')) throw new ApiError('服务器响应缺少数据', response.status);
      return result.data;
    } catch (error) {
      if (error instanceof ApiError) throw error;
      if (timedOut) throw new ApiError('请求超时，请稍后重试');
      if (requestOptions.signal?.aborted) throw error;
      throw new ApiError('网络连接失败，请检查网络后重试');
    } finally {
      clearTimeout(timer);
      requestOptions.signal?.removeEventListener('abort', abort);
    }
  };
}

export const api = createApiClient({
  baseUrl: import.meta.env.VITE_API_BASE_URL || '/api',
  token: () => localStorage.getItem('token'),
  onUnauthorized: () => {
    localStorage.removeItem('token');
    localStorage.removeItem('user');
    window.dispatchEvent(new Event('auth:unauthorized'));
  },
});
