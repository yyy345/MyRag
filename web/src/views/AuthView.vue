<script setup lang="ts">
import { computed, onMounted, ref } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { api } from '../api/client';
import { useAuthStore } from '../stores/auth';

const auth = useAuthStore();
const props = withDefaults(defineProps<{ mode?: 'login' | 'register' }>(), { mode: 'login' });
const registering = computed(() => props.mode === 'register');
const route = useRoute();
const router = useRouter();
const username = ref(''); const email = ref(''); const password = ref(''); const captchaCode = ref('');
const captcha = ref<{ id: string; svg: string } | null>(null);
const captchaLoading = ref(false); const error = ref('');
async function refreshCaptcha() {
  if (captchaLoading.value) return;
  captchaLoading.value = true; captcha.value = null; captchaCode.value = '';
  try { captcha.value = await api('/auth/captcha'); }
  catch (err) { error.value = err instanceof Error ? err.message : '验证码加载失败，请重试'; }
  finally { captchaLoading.value = false; }
}
async function submit() {
  if (auth.loading || !captcha.value) return;
  error.value = '';
  if (!username.value.trim()) { error.value = '请输入用户名'; return; }
  try {
    const body = { username: username.value.trim(), password: password.value, captchaId: captcha.value.id, captchaCode: captchaCode.value.trim() };
    if (registering.value) await auth.register({ ...body, email: email.value.trim() });
    else await auth.login(body);
    const next = route.query.redirect;
    await router.replace(typeof next === 'string' && next.startsWith('/') && !next.startsWith('//') ? next : '/');
  } catch (err) { error.value = err instanceof Error ? err.message : '登录失败'; await refreshCaptcha(); }
}
onMounted(refreshCaptcha);
</script>

<template>
  <main class="auth-page"><section class="auth-panel">
    <h1>{{ registering ? '创建账号' : 'AI 知识库平台' }}</h1><p class="muted">{{ registering ? '加入 AI 知识库平台' : '登录后管理资料，开始知识问答。' }}</p>
    <p v-if="error || auth.restoreError" role="alert" class="error">{{ error || auth.restoreError }}</p>
    <form @submit.prevent="submit">
      <label for="username">{{ registering ? '用户名' : '用户名 / 邮箱' }}</label><input id="username" v-model="username" autocomplete="username" required />
      <template v-if="registering"><label for="email">邮箱</label><input id="email" v-model="email" type="email" autocomplete="email" required /></template>
      <label for="password">密码</label><input id="password" v-model="password" type="password" :minlength="registering ? 6 : undefined" :autocomplete="registering ? 'new-password' : 'current-password'" required />
      <label for="captcha">验证码</label>
      <div class="captcha-row"><input id="captcha" v-model="captchaCode" required autocomplete="off" />
        <button type="button" class="captcha-button" :disabled="captchaLoading || auth.loading" aria-label="刷新验证码" @click="refreshCaptcha">
          <img v-if="captcha" :src="`data:image/svg+xml;charset=utf-8,${encodeURIComponent(captcha.svg)}`" alt="数学验证码" />
          <span v-else>{{ captchaLoading ? '加载中…' : '重新加载' }}</span>
        </button>
      </div>
      <button class="primary" type="submit" :disabled="auth.loading || !captcha">{{ auth.loading ? '处理中…' : registering ? '注册' : '登录' }}</button>
    </form>
    <p v-if="registering">已有账号？<RouterLink to="/login">立即登录</RouterLink></p>
    <p v-else>还没有账号？<RouterLink to="/register">立即注册</RouterLink></p>
  </section></main>
</template>
