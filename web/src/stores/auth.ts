import { defineStore } from 'pinia';
import type { AuthResponse, User, LoginRequest, RegisterRequest } from 'shared';
import { api } from '../api/client';

export const useAuthStore = defineStore('auth', {
  state: () => ({ token: localStorage.getItem('token'), user: null as User | null, loading: false, restoreError: '' }),
  actions: {
    setSession(session: AuthResponse) {
      this.token = session.token; this.user = session.user; this.restoreError = '';
      localStorage.setItem('token', session.token);
      localStorage.setItem('user', JSON.stringify(session.user));
    },
    logout() {
      this.token = null; this.user = null; this.restoreError = '';
      localStorage.removeItem('token'); localStorage.removeItem('user');
    },
    async restore() {
      if (!this.token || this.user) return;
      try { this.user = await api<User>('/auth/me'); this.restoreError = ''; }
      catch (error) {
        this.restoreError = error instanceof Error ? error.message : '无法验证登录状态';
        if (!localStorage.getItem('token')) this.logout();
      }
    },
    async login(body: LoginRequest) {
      if (this.loading) return;
      this.loading = true;
      try { this.setSession(await api<AuthResponse>('/auth/login', { method: 'POST', body })); }
      finally { this.loading = false; }
    },
    async register(body: RegisterRequest) {
      if (this.loading) return;
      this.loading = true;
      try { this.setSession(await api<AuthResponse>('/auth/register', { method: 'POST', body })); }
      finally { this.loading = false; }
    },
  },
});
