<script setup lang="ts">
import { onBeforeUnmount, onMounted, reactive, ref } from 'vue';
import type { KnowledgeBase } from 'shared';
import { knowledgeApi } from '../api/knowledge';
import ModalDialog from '../components/ModalDialog.vue';

const records = ref<KnowledgeBase[]>([]); const total = ref(0); const page = ref(1); const size = 10;
const loading = ref(false); const saving = ref(false); const error = ref(''); const formError = ref('');
const editorOpen = ref(false); const editing = ref<string | null>(null); const deleting = ref<KnowledgeBase | null>(null);
const form = reactive({ name: '', description: '', system_prompt: '' });
let request: AbortController | undefined;
async function load() {
  request?.abort(); const current = new AbortController(); request = current;
  loading.value = true; error.value = '';
  try {
    const result = await knowledgeApi.list(page.value, size, current.signal);
    if (current.signal.aborted) return;
    records.value = result.records; total.value = result.total;
    if (!result.records.length && page.value > 1) { page.value--; await load(); }
  } catch (err) { if (!current.signal.aborted) error.value = err instanceof Error ? err.message : '知识库加载失败'; }
  finally { if (request === current) loading.value = false; }
}
function edit(kb?: KnowledgeBase) {
  editing.value = kb?.id || null; form.name = kb?.name || ''; form.description = kb?.description || ''; form.system_prompt = kb?.system_prompt || '';
  formError.value = ''; editorOpen.value = true;
}
async function save() {
  if (saving.value) return;
  if (!form.name.trim()) { formError.value = '请输入知识库名称'; return; }
  saving.value = true; formError.value = '';
  try {
    const body = { ...form, name: form.name.trim() };
    if (editing.value) await knowledgeApi.update(editing.value, body); else { await knowledgeApi.create(body); page.value = 1; }
    editorOpen.value = false; await load();
  } catch (err) { formError.value = err instanceof Error ? err.message : '保存失败'; }
  finally { saving.value = false; }
}
async function remove() {
  if (!deleting.value || saving.value) return;
  saving.value = true; formError.value = '';
  try { await knowledgeApi.delete(deleting.value.id); deleting.value = null; await load(); }
  catch (err) { formError.value = err instanceof Error ? err.message : '删除失败'; }
  finally { saving.value = false; }
}
function changePage(delta: number) { page.value += delta; void load(); }
onMounted(load); onBeforeUnmount(() => request?.abort());
</script>

<template>
  <section class="page">
    <div class="page-heading"><div><h1>我的知识库</h1><p class="muted">按主题整理资料，连接知识与问答。</p></div><button class="primary" @click="edit()">新建知识库</button></div>
    <div v-if="error" role="alert" class="error">{{ error }} <button @click="load">重试</button></div>
    <p v-if="loading" role="status">正在加载知识库…</p>
    <p v-else-if="!records.length && !error" class="empty">还没有知识库。点击“新建知识库”，开始整理资料。</p>
    <div v-else class="library-grid" :aria-busy="loading">
      <article v-for="kb in records" :key="kb.id" class="library-card" :aria-label="kb.name">
        <h2><RouterLink :to="`/knowledge-bases/${kb.id}`">{{ kb.name }}</RouterLink></h2>
        <p class="description">{{ kb.description || '暂无描述' }}</p>
        <div class="actions"><RouterLink :to="`/knowledge-bases/${kb.id}/chat`">开始问答</RouterLink><button @click="edit(kb)">编辑</button><button @click="deleting = kb; formError = ''">删除</button></div>
      </article>
    </div>
    <nav v-if="total > 0" class="pagination" aria-label="知识库分页">
      <span>共 {{ total }} 个知识库，第 {{ page }} / {{ Math.max(1, Math.ceil(total / size)) }} 页</span>
      <button :disabled="loading || page === 1" @click="changePage(-1)">上一页</button><button :disabled="loading || page * size >= total" @click="changePage(1)">下一页</button>
    </nav>
    <ModalDialog :open="editorOpen" :title="editing ? '编辑知识库' : '新建知识库'" :busy="saving" @close="editorOpen = false">
      <p v-if="formError" role="alert" class="error">{{ formError }}</p>
      <form class="editor-form" @submit.prevent="save">
        <label for="kb-name">名称</label><input id="kb-name" v-model="form.name" required maxlength="200" />
        <label for="kb-description">描述</label><textarea id="kb-description" v-model="form.description" rows="3" />
        <label for="kb-prompt">回答要求（可选）</label><textarea id="kb-prompt" v-model="form.system_prompt" rows="3" placeholder="例如：回答时引用资料来源" />
        <div class="actions"><button type="button" :disabled="saving" @click="editorOpen = false">取消</button><button class="primary" :disabled="saving">{{ saving ? '保存中…' : '保存' }}</button></div>
      </form>
    </ModalDialog>
    <ModalDialog :open="!!deleting" title="删除知识库" :busy="saving" @close="deleting = null">
      <p>确定删除“{{ deleting?.name }}”？其中的知识条目和会话也会被删除。</p>
      <p v-if="formError" role="alert" class="error">{{ formError }}</p>
      <div class="actions"><button :disabled="saving" @click="deleting = null">取消</button><button class="danger" :disabled="saving" @click="remove">确认删除</button></div>
    </ModalDialog>
  </section>
</template>
