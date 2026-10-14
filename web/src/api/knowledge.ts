import type { KnowledgeBase, CreateKnowledgeBaseRequest, PaginatedData, KnowledgeEntry, CreateEntryRequest, EntryStatus } from 'shared';
import { api } from './client';

export const knowledgeApi = {
  list: (page = 1, size = 10, signal?: AbortSignal) => api<PaginatedData<KnowledgeBase>>('/knowledge-bases', { query: { page, size }, signal }),
  get: (id: string, signal?: AbortSignal) => api<KnowledgeBase>(`/knowledge-bases/${encodeURIComponent(id)}`, { signal }),
  create: (body: CreateKnowledgeBaseRequest) => api<KnowledgeBase>('/knowledge-bases', { method: 'POST', body }),
  update: (id: string, body: Partial<CreateKnowledgeBaseRequest>) => api<KnowledgeBase>(`/knowledge-bases/${encodeURIComponent(id)}`, { method: 'PUT', body }),
  delete: (id: string) => api<null>(`/knowledge-bases/${encodeURIComponent(id)}`, { method: 'DELETE' }),
};

const entriesPath = (kb: string) => `/knowledge-bases/${encodeURIComponent(kb)}/entries`;
export const entryApi = {
  list: (kb: string, query: { page: number; size: number; search?: string; status?: string }, signal?: AbortSignal) => api<PaginatedData<KnowledgeEntry>>(entriesPath(kb), { query, signal }),
  create: (kb: string, body: CreateEntryRequest) => api<KnowledgeEntry>(entriesPath(kb), { method: 'POST', body }),
  rename: (kb: string, id: string, title: string) => api<KnowledgeEntry>(`${entriesPath(kb)}/${encodeURIComponent(id)}`, { method: 'PUT', body: { title } }),
  status: (kb: string, id: string, status: EntryStatus) => api<KnowledgeEntry>(`${entriesPath(kb)}/${encodeURIComponent(id)}/status`, { method: 'PATCH', body: { status } }),
  delete: (kb: string, id: string) => api<null>(`${entriesPath(kb)}/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  upload: (kb: string, file: File, signal?: AbortSignal) => {
    const body = new FormData(); body.append('file', file);
    return api<{ entry_id: string; processing_status: string }>(`${entriesPath(kb)}/upload`, { method: 'POST', body, signal });
  },
  uploadImages: (kb: string, files: File[], signal?: AbortSignal) => {
    const body = new FormData(); files.forEach(file => body.append('images', file));
    return api<{ entries: Array<{ entry_id: string; title: string }> }>(`${entriesPath(kb)}/upload-images`, { method: 'POST', body, signal });
  },
};
