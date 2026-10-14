import { createApp } from 'vue';
import { createPinia } from 'pinia';
import App from './App.vue';
import { router } from './router';
import { useAuthStore } from './stores/auth';
import './style.css';

const pinia = createPinia();
window.addEventListener('auth:unauthorized', () => {
  useAuthStore(pinia).logout();
  if (router.currentRoute.value.meta.requiresAuth) void router.replace({ path: '/login', query: { redirect: router.currentRoute.value.fullPath } });
});
createApp(App).use(pinia).use(router).mount('#app');
