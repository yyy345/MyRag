import axios from 'axios';
import type { ApiResponse } from 'shared';

const client = axios.create({
  baseURL: '/api',
  timeout: 60000,
});

/**
 * 请求拦截器：自动附带 JWT token。
 */
client.interceptors.request.use((config) => {
  const token = localStorage.getItem('token');
  if (token) {
    config.headers.Authorization = `Bearer ${token}`;
  }
  return config;
});

/**
 * 响应拦截器：401 时清除登录态并跳转登录页。
 */
client.interceptors.response.use(
  (response) => response,
  (error) => {
    const isAuthEndpoint = error.config?.url?.includes('/auth/login') || error.config?.url?.includes('/auth/register');
    if (error.response?.status === 401 && !isAuthEndpoint) {
      localStorage.removeItem('token');
      localStorage.removeItem('user');
      window.location.href = '/login';
      return Promise.reject(new Error('请先登录'));
    }
    const message =
      error.response?.data?.message ||
      error.response?.data?.error ||
      error.message;
    return Promise.reject(new Error(message));
  },
);

export async function get<T>(
  url: string,
  params?: Record<string, unknown>,
): Promise<ApiResponse<T>> {
  const { data } = await client.get<ApiResponse<T>>(url, { params });
  return data;
}

export async function post<T>(
  url: string,
  body?: unknown,
): Promise<ApiResponse<T>> {
  const { data } = await client.post<ApiResponse<T>>(url, body);
  return data;
}

export async function put<T>(
  url: string,
  body?: unknown,
): Promise<ApiResponse<T>> {
  const { data } = await client.put<ApiResponse<T>>(url, body);
  return data;
}

export async function del<T>(
  url: string,
): Promise<ApiResponse<T>> {
  const { data } = await client.delete<ApiResponse<T>>(url);
  return data;
}

export async function patch<T>(
  url: string,
  body?: unknown,
): Promise<ApiResponse<T>> {
  const { data } = await client.patch<ApiResponse<T>>(url, body);
  return data;
}

/**
 * SSE 流式请求（使用原生 fetch，axios 不支持流式读取）。
 * 返回 AsyncGenerator，逐条产出解析后的 JSON 事件。
 */
export async function* postStream<T>(
  url: string,
  body: unknown,
): AsyncGenerator<T> {
  const token = localStorage.getItem('token');
  const response = await fetch(`/api${url}`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    },
    body: JSON.stringify(body),
  });

  if (!response.ok) {
    if (response.status === 401) {
      localStorage.removeItem('token');
      localStorage.removeItem('user');
      window.location.href = '/login';
      throw new Error('请先登录');
    }
    const error = await response.json().catch(() => null);
    throw new Error(typeof error?.message === 'string' && error.message.trim()
      ? error.message
      : `回答请求失败（${response.status}），请稍后重试`);
  }

  if (!response.body || !response.headers.get('Content-Type')?.includes('text/event-stream')) {
    throw new Error('回答服务返回了无效响应，请稍后重试');
  }
  const reader = response.body.getReader();
  const decoder = new TextDecoder('utf-8', { fatal: true });
  let buffer = '';
  let data: string[] = [];

  try {
    while (true) {
      const { done, value } = await reader.read();
      buffer += done ? decoder.decode() : decoder.decode(value, { stream: true });
      const lines = buffer.split('\n');
      buffer = lines.pop() || '';
      for (const rawLine of lines) {
        const line = rawLine.replace(/\r$/, '');
        if (line === '' && data.length) {
          let event: T;
          try {
            event = JSON.parse(data.join('\n')) as T;
          } catch {
            throw new Error('回答数据格式异常，请重新发送');
          }
          data = [];
          yield event;
        } else if (line.startsWith('data:')) {
          data.push(line.slice(5).replace(/^ /, ''));
        }
      }
      if (done) {
        if (buffer.trim() || data.length) throw new Error('回答连接中断，请重新发送');
        break;
      }
    }
  } finally {
    await reader.cancel().catch(() => {});
    reader.releaseLock();
  }
}

export { client };
