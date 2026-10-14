import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router';
import AppLayout from '../components/AppLayout.vue';
import NotFoundView from '../views/NotFoundView.vue';
import { useAuthStore } from '../stores/auth';

export const routes: RouteRecordRaw[] = [
  { path: '/login', component: () => import('../views/AuthView.vue'), meta: { title: '登录' } },
  { path: '/register', component: () => import('../views/AuthView.vue'), props: { mode: 'register' }, meta: { title: '注册' } },
  { path: '/', component: AppLayout, meta: { requiresAuth: true }, children: [
    { path: '', component: () => import('../views/KnowledgeBasesView.vue'), meta: { title: '我的知识库' } },
    { path: 'knowledge-bases/:id', component: () => import('../views/EntriesView.vue'), meta: { title: '知识条目管理' } },
    { path: 'knowledge-bases/:id/chat/:sid?', component: () => import('../views/ChatView.vue'), meta: { title: '对话问答' } },
    { path: 'analytics', component: () => import('../views/AnalyticsView.vue'), meta: { title: '数据统计' } },
  ] },
  { path: '/:pathMatch(.*)*', component: NotFoundView, meta: { title: '页面不存在' } },
];

export const router = createRouter({ history: createWebHistory(), routes });
router.beforeEach(async to => {
  const auth = useAuthStore();
  if (auth.token && !auth.user) await auth.restore();
  if (to.meta.requiresAuth && !auth.user) return { path: '/login', query: { redirect: to.fullPath } };
  if ((to.path === '/login' || to.path === '/register') && auth.user) return '/';
});
router.afterEach(to => { document.title = `${String(to.meta.title || 'AI 知识库')} · AI 知识库`; });
