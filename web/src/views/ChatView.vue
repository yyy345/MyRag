<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import type { ChatMessage, ChatSession } from 'shared';
import { chatApi } from '../api/chat';
import { knowledgeApi } from '../api/knowledge';
import MessageBubble from '../components/MessageBubble.vue';
import ModalDialog from '../components/ModalDialog.vue';
import { streamChat } from '../api/sse';
import { useSSE } from '../composables/useSSE';

const route = useRoute(); const router = useRouter();
const kbId = computed(() => String(route.params.id));
const sid = computed(() => String(route.params.sid || ''));
const name = ref(''); const sessions = ref<ChatSession[]>([]); const messages = ref<ChatMessage[]>([]);
const loading = ref(false); const busy = ref(false); const error = ref(''); const formError = ref('');
const deleting = ref<ChatSession | null>(null);
const extracting = ref(false); const extractionNotice = ref(''); const extractionError = ref('');
let extractionController: AbortController | undefined;
const draft = ref(''); const sending = ref(false); const historyPanel = ref<HTMLElement>(); const following = ref(true);
const { streaming, content: answer, error: streamError, stopped, references, usage, start, cancel } = useSSE();
const liveMessage = computed<ChatMessage>(() => ({ id: 'stream', session_id: sid.value, role: 'ASSISTANT', content: answer.value, references: references.value, token_usage: usage.value, created_at: '' }));
let pendingLoad: Promise<void> = Promise.resolve();
let controller: AbortController | undefined; let disposed = false;
const sessionUrl = (id = '') => `/knowledge-bases/${encodeURIComponent(kbId.value)}/chat${id ? `/${encodeURIComponent(id)}` : ''}`;
async function load() {
  controller?.abort(); const current = new AbortController(); controller = current;
  loading.value = true; error.value = ''; messages.value = [];
  const kb = kbId.value; const selected = sid.value;
  try {
    const [base, list] = await Promise.all([knowledgeApi.get(kb, current.signal), chatApi.list(kb, current.signal)]);
    if (current.signal.aborted) return;
    name.value = base.name; sessions.value = list;
    if (selected) {
      if (!list.some(session => session.id === selected)) throw new Error('对话不存在或不属于当前知识库');
      const history = await chatApi.messages(kb, selected, current.signal);
      if (!current.signal.aborted) messages.value = history;
    }
  } catch (err) { if (!current.signal.aborted) error.value = err instanceof Error ? err.message : '对话加载失败'; }
  finally { if (controller === current) loading.value = false; }
}
async function create() {
  if (busy.value) return;
  busy.value = true; error.value = ''; const path = route.fullPath;
  try {
    const session = await chatApi.create(kbId.value);
    if (!disposed && route.fullPath === path) await router.push(sessionUrl(session.id));
  } catch (err) { if (!disposed && route.fullPath === path) error.value = err instanceof Error ? err.message : '创建失败'; }
  finally { busy.value = false; }
}
async function scrollLatest() { following.value = true; await nextTick(); const panel = historyPanel.value; if (panel) panel.scrollTop = panel.scrollHeight; }
function trackScroll() { const panel = historyPanel.value; if (panel) following.value = panel.scrollHeight - panel.scrollTop - panel.clientHeight < 80; }
watch([answer, messages], async () => { if (following.value) await scrollLatest(); }, { flush: 'post' });
async function send() {
  const question = draft.value.trim();
  if (!question || sending.value || streaming.value || loading.value || busy.value || error.value) return;
  sending.value = true; const kb = kbId.value; const initialPath = route.fullPath;
  try {
    let selected = sid.value;
    if (!selected) {
      const session = await chatApi.create(kb);
      if (disposed || route.fullPath !== initialPath) return;
      selected = session.id;
      await router.push(sessionUrl(selected)); await pendingLoad;
    }
    if (disposed || kbId.value !== kb || sid.value !== selected || error.value) return;
    const path = route.fullPath;
    draft.value = '';
    messages.value = [...messages.value, { id: crypto.randomUUID(), session_id: selected, role: 'USER', content: question, references: null, token_usage: null, created_at: new Date().toISOString() }];
    await scrollLatest();
    const complete = await start(signal => streamChat(kb, selected, question, signal));
    if (complete && !disposed && route.fullPath === path) {
      await load();
      if (!disposed && route.fullPath === path && !error.value) cancel(true);
    }
  } catch (err) { if (!disposed && kbId.value === kb) streamError.value = err instanceof Error ? err.message : '发送失败，请重试'; }
  finally { sending.value = false; }
}
async function remove() {
  if (!deleting.value || busy.value) return;
  const target = deleting.value; const kb = kbId.value;
  busy.value = true; formError.value = '';
  try {
    await chatApi.delete(kb, target.id);
    if (disposed || kbId.value !== kb) return;
    deleting.value = null;
    if (sid.value === target.id) await router.replace(sessionUrl());
    else await load();
  } catch (err) { if (!disposed && kbId.value === kb) formError.value = err instanceof Error ? err.message : '删除失败，请重试'; }
  finally { busy.value = false; }
}
async function extract() {
  if (extracting.value) return;
  extracting.value = true; extractionNotice.value = ''; extractionError.value = '';
  const current = new AbortController(); extractionController = current;
  try {
    const result = await chatApi.extract(kbId.value, sid.value || undefined, current.signal);
    if (!current.signal.aborted) extractionNotice.value = result.entries.length ? `已提炼 ${result.entries.length} 条知识，请返回知识条目查看处理状态。` : '未提炼到新知识';
  } catch (err) { if (!current.signal.aborted) extractionError.value = err instanceof Error ? err.message : '提炼失败，请重试'; }
  finally { if (extractionController === current) extracting.value = false; }
}
watch(() => [kbId.value, sid.value], ([kb], previous) => {
  extractionController?.abort(); extracting.value = false; extractionNotice.value = ''; extractionError.value = '';
  cancel(true); draft.value = ''; following.value = true;
  if (!previous || previous[0] !== kb) { name.value = ''; sessions.value = []; }
  deleting.value = null; formError.value = ''; pendingLoad = load();
}, { immediate: true });
onBeforeUnmount(() => { disposed = true; controller?.abort(); extractionController?.abort(); });
</script>

<template>
  <section class="page">
    <RouterLink :to="`/knowledge-bases/${encodeURIComponent(kbId)}`">返回知识条目</RouterLink>
    <div class="page-heading"><div><h1>对话问答</h1><p class="muted">{{ name }}</p></div><div class="actions"><button :disabled="extracting || loading || sending || streaming || busy || !name || !!error" @click="extract">{{ extracting ? '正在提炼…' : sid ? '提炼当前对话' : '提炼全部对话' }}</button><button :disabled="loading || sending || streaming" @click="cancel(true); load()">刷新对话</button></div></div>
    <p v-if="extractionNotice" role="status">{{ extractionNotice }}</p><p v-if="extractionError" role="alert" class="error">{{ extractionError }}</p>
    <div v-if="error" class="error" role="alert">{{ error }} <button @click="load">重试</button></div>
    <div class="chat-layout">
      <aside class="session-panel" aria-label="对话列表">
        <button class="primary" :disabled="busy || loading || sending || streaming || !name" @click="create">新对话</button>
        <p v-if="!loading && !sessions.length" class="muted">暂无对话</p>
        <ul class="session-list"><li v-for="session in sessions" :key="session.id" :class="{ active: session.id === sid }">
          <RouterLink :to="sessionUrl(session.id)" :aria-current="session.id === sid ? 'page' : undefined">{{ session.title }}</RouterLink>
          <button class="danger" :disabled="busy || sending || streaming" :aria-label="`删除对话 ${session.title}`" @click="deleting = session; formError = ''">删除</button>
        </li></ul>
      </aside>
      <div class="conversation" :aria-busy="loading">
        <div ref="historyPanel" class="chat-history" @scroll="trackScroll">
        <p v-if="loading" role="status">正在加载对话…</p>
        <p v-else-if="!sid" class="empty">选择一个对话，或创建新对话。</p>
        <p v-else-if="!error && !messages.length" class="empty">当前对话暂无消息。</p>
        <ol class="message-list" aria-label="历史消息"><li v-for="message in messages" :key="message.id"><MessageBubble :message="message" /></li></ol>
        <MessageBubble v-if="answer" :message="liveMessage" />
        <p v-if="streaming" role="status">正在生成回答…</p>
        <p v-if="stopped" role="status">已停止生成，当前回答尚未完成。</p>
        <p v-if="streamError" class="error" role="alert">{{ streamError }}（本次回答未完成）</p>
        </div>
        <button v-if="!following" @click="scrollLatest">回到最新消息</button>
        <form class="chat-composer" @submit.prevent="send"><label for="chat-question">问题</label><textarea id="chat-question" v-model="draft" rows="3" placeholder="输入问题，结合知识库资料进行问答" :disabled="sending || streaming || busy" @keydown.ctrl.enter.prevent="send" /><div class="actions"><button v-if="streaming" type="button" @click="cancel()">停止生成</button><button class="primary" :disabled="!draft.trim() || loading || sending || streaming || busy || !!error">发送</button></div></form>
      </div>
    </div>
    <ModalDialog :open="!!deleting" title="删除对话" :busy="busy" @close="deleting = null"><p>确定删除“{{ deleting?.title }}”及其所有消息？此操作无法撤销。</p><p v-if="formError" class="error" role="alert">{{ formError }}</p><div class="actions"><button :disabled="busy" @click="deleting = null">取消</button><button class="danger" :disabled="busy" @click="remove">确认删除</button></div></ModalDialog>
  </section>
</template>
