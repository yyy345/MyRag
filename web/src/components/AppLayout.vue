<script setup lang="ts">
import { computed, ref } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { useAuthStore } from '../stores/auth';
const route = useRoute();
const router = useRouter();
const auth = useAuthStore();
function logout() { auth.logout(); void router.replace('/login'); }
const menuOpen = ref(false);
const inLibrary = computed(() => route.path === '/' || route.path.startsWith('/knowledge-bases/'));
</script>

<template>
  <div class="app-shell">
    <a class="skip-link" href="#main-content">跳到内容</a>
    <header class="mobile-bar">
      <span>AI 知识库</span>
      <button type="button" :aria-expanded="menuOpen" aria-controls="sidebar" @click="menuOpen = !menuOpen">导航菜单</button>
    </header>
    <aside id="sidebar" class="sidebar" :class="{ open: menuOpen }">
      <RouterLink class="brand" to="/" @click="menuOpen = false">AI 知识库</RouterLink>
      <nav aria-label="主导航">
        <RouterLink to="/" :class="{ selected: inLibrary }" :aria-current="inLibrary ? 'page' : undefined" @click="menuOpen = false">我的知识库</RouterLink>
        <RouterLink to="/analytics" :class="{ selected: route.path === '/analytics' }" @click="menuOpen = false">数据统计</RouterLink>
      </nav>
      <div class="account"><span>{{ auth.user?.username }}</span><button type="button" @click="logout">退出登录</button></div>
    </aside>
    <main id="main-content" class="workspace"><RouterView /></main>
  </div>
</template>
