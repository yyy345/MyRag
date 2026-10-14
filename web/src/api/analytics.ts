import { api } from './client';

export interface Overview {
  hotEntries: Array<{ entry_title: string; kb_name: string; total_hits: number }>;
  kbActivity: Array<{ kb_id: string; kb_name: string; total_hits: number; entry_count: number }>;
  summary: { kbCount: number; entryCount: number; sessionCount: number };
}
export const analyticsApi = { overview: (signal?: AbortSignal) => api<Overview>('/analytics/overview', { signal }) };
