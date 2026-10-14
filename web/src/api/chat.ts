import type { ChatMessage, ChatSession } from 'shared';
import { api } from './client';

export const sessionsPath = (kb: string) => `/knowledge-bases/${encodeURIComponent(kb)}/chat/sessions`;
export const chatApi = {
  list: (kb: string, signal?: AbortSignal) => api<ChatSession[]>(sessionsPath(kb), { signal }),
  create: (kb: string) => api<ChatSession>(sessionsPath(kb), { method: 'POST' }),
  messages: (kb: string, sid: string, signal?: AbortSignal) => api<ChatMessage[]>(`${sessionsPath(kb)}/${encodeURIComponent(sid)}`, { signal }),
  delete: (kb: string, sid: string) => api<null>(`${sessionsPath(kb)}/${encodeURIComponent(sid)}`, { method: 'DELETE' }),
  extract: (kb: string, sessionId?: string, signal?: AbortSignal) => api<{ entries: Array<{ id: string; title: string }>; count?: number }>(`/knowledge-bases/${encodeURIComponent(kb)}/chat/extract`, { method: 'POST', body: { sessionId }, signal }),
};
