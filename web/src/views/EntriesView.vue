<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { useRoute } from 'vue-router';
import type { KnowledgeEntry, EntryStatus } from 'shared';
import { entryApi, knowledgeApi } from '../api/knowledge';
import ModalDialog from '../components/ModalDialog.vue';
import MarkdownContent from '../components/MarkdownContent.vue';
import FileUpload from '../components/FileUpload.vue';

const route = useRoute(); const kbId = computed(() => String(route.params.id));
const name = ref(''); const records = ref<KnowledgeEntry[]>([]); const page = ref(1); const total = ref(0);
const search = ref(''); const status = ref(''); const selected = ref<string[]>([]);
const loading = ref(false); const busy = ref(false); const error = ref(''); const formError = ref('');
const editorOpen = ref(false); const editing = ref<KnowledgeEntry | null>(null); const title = ref(''); const content = ref('');
const detail = ref<KnowledgeEntry | null>(null); const preview = ref<KnowledgeEntry | null>(null);
const deleting = ref<string[]>([]); const deleteOpen = ref(false);
const uploadOpen = ref(false); const uploading = ref(false);
const statusLabels = { PENDING: '待处理', PROCESSING: '处理中', COMPLETED: '已完成', FAILED: '失败' };
let controller: AbortController | undefined; let generation = 0;
let pollTimer: ReturnType<typeof setTimeout> | undefined;
let disposed = false;
const extension = computed(() => editing.value?.source_file_name?.match(/\.[^.]+$/)?.[0].toLowerCase() || '');
function canPreview(entry: KnowledgeEntry) { return !entry.source_file_name || /\.(md|txt|png|jpe?g)$/i.test(entry.source_file_name); }
async function load() {
  clearTimeout(pollTimer);
  controller?.abort(); const current = new AbortController(); controller = current;
  loading.value = true; error.value = '';
  try {
    const result = await entryApi.list(kbId.value, { page: page.value, size: 10, search: search.value.trim(), status: status.value }, current.signal);
    if (current.signal.aborted) return;
    records.value = result.records; total.value = result.total;
    selected.value = selected.value.filter(id => result.records.some(entry => entry.id === id));
    if (!records.value.length && page.value > 1) { page.value--; await load(); }
  } catch (err) { if (!current.signal.aborted) error.value = err instanceof Error ? err.message : '条目加载失败'; }
  finally {
    if (controller === current) {
      loading.value = false;
      if (!disposed && !current.signal.aborted && !error.value && records.value.some(entry => ['PENDING', 'PROCESSING'].includes(entry.processing_status))) pollTimer = setTimeout(load, 3000);
    }
  }
}
function filter() { page.value = 1; selected.value = []; void load(); }
function edit(entry?: KnowledgeEntry) {
  editing.value = entry || null; content.value = ''; formError.value = '';
  const suffix = entry?.source_file_name?.match(/\.[^.]+$/)?.[0] || '';
  title.value = entry?.title || '';
  if (suffix && title.value.toLowerCase().endsWith(suffix.toLowerCase())) title.value = title.value.slice(0, -suffix.length);
  editorOpen.value = true;
}
async function save() {
  if (busy.value) return;
  if (!title.value.trim() || (!editing.value && !content.value.trim())) { formError.value = '请填写标题和内容'; return; }
  busy.value = true; formError.value = '';
  try {
    if (editing.value) await entryApi.rename(kbId.value, editing.value.id, `${title.value.trim()}${extension.value}`);
    else { await entryApi.create(kbId.value, { title: title.value.trim(), content: content.value }); page.value = 1; }
    editorOpen.value = false; await load();
  } catch (err) { formError.value = err instanceof Error ? err.message : '保存失败'; }
  finally { busy.value = false; }
}
async function changeStatus(entry: KnowledgeEntry) {
  if (busy.value) return; busy.value = true;
  try { await entryApi.status(kbId.value, entry.id, (entry.status === 'ENABLED' ? 'DISABLED' : 'ENABLED') as EntryStatus); await load(); }
  catch (err) { error.value = err instanceof Error ? err.message : '状态更新失败'; }
  finally { busy.value = false; }
}
function confirmDelete(ids: string[]) { deleting.value = [...ids]; formError.value = ''; deleteOpen.value = true; }
async function remove() {
  if (busy.value) return; busy.value = true; formError.value = '';
  const failed: string[] = [];
  for (const id of deleting.value) { try { await entryApi.delete(kbId.value, id); } catch { failed.push(id); } }
  deleting.value = failed; selected.value = failed;
  if (failed.length) formError.value = `${failed.length} 个条目删除失败，可再次重试。`;
  else deleteOpen.value = false;
  await load(); busy.value = false;
}
watch(kbId, async id => {
  const version = ++generation; name.value = ''; page.value = 1; search.value = ''; status.value = ''; selected.value = [];
  editorOpen.value = false; deleteOpen.value = false; uploadOpen.value = false; detail.value = null; preview.value = null;
  void load();
  try { const kb = await knowledgeApi.get(id); if (generation === version) name.value = kb.name; }
  catch (err) { if (generation === version) error.value = err instanceof Error ? err.message : '知识库加载失败'; }
}, { immediate: true });
onBeforeUnmount(() => { disposed = true; generation++; clearTimeout(pollTimer); controller?.abort(); });
</script>

<template>
  <section class="page">
    <RouterLink to="/">返回知识库</RouterLink>
    <div class="page-heading"><div><h1>{{ name || '知识条目管理' }}</h1><p class="muted">管理条目与资料的使用状态。</p></div><div class="actions"><RouterLink :to="`/knowledge-bases/${kbId}/chat`">开始问答</RouterLink><button @click="uploadOpen = true">上传文件</button><button class="primary" @click="edit()">新建条目</button></div></div>
    <form class="toolbar" @submit.prevent="filter"><label for="entry-search">搜索</label><input id="entry-search" v-model="search" placeholder="标题或内容" /><label for="entry-status">状态</label><select id="entry-status" v-model="status" @change="filter"><option value="">全部</option><option value="ENABLED">启用</option><option value="DISABLED">禁用</option></select><button>查询</button><button type="button" :disabled="loading" @click="load">刷新</button></form>
    <div v-if="error" role="alert" class="error">{{ error }} <button @click="load">重试</button></div>
    <p v-if="loading" role="status">正在加载条目…</p>
    <button v-if="selected.length" class="danger" :disabled="busy" @click="confirmDelete(selected)">删除选中（{{ selected.length }}）</button>
    <div class="table-scroll"><table :aria-busy="loading"><caption class="sr-only">知识条目列表</caption><thead><tr><th>选择</th><th>标题</th><th>类型</th><th>处理状态</th><th>使用状态</th><th>操作</th></tr></thead>
      <tbody><tr v-for="entry in records" :key="entry.id">
        <td><input v-model="selected" type="checkbox" :value="entry.id" :aria-label="`选择 ${entry.title}`" :disabled="busy" /></td><td class="entry-title">{{ entry.title }}</td><td>{{ entry.type === 'MANUAL' ? '手动' : '文件' }}</td>
        <td><span :class="{ 'failed-status': entry.processing_status === 'FAILED' }" :title="entry.processing_message || ''">{{ statusLabels[entry.processing_status] }}</span></td>
        <td><button :disabled="busy" :aria-label="`${entry.status === 'ENABLED' ? '禁用' : '启用'} ${entry.title}`" @click="changeStatus(entry)">{{ entry.status === 'ENABLED' ? '已启用' : '已禁用' }}</button></td>
        <td><div class="actions"><button v-if="canPreview(entry)" @click="preview = entry">预览</button><button @click="detail = entry">详情</button><button :disabled="busy" @click="edit(entry)">编辑</button><button class="danger" :disabled="busy" @click="confirmDelete([entry.id])">删除</button></div></td>
      </tr></tbody></table></div>
    <p v-if="!loading && !error && !records.length" class="empty">没有符合条件的条目。可以新建条目或调整筛选条件。</p>
    <nav v-if="total > 0" class="pagination" aria-label="条目分页"><span>共 {{ total }} 条，第 {{ page }} / {{ Math.max(1, Math.ceil(total / 10)) }} 页</span><button :disabled="loading || page === 1" @click="page--; load()">上一页</button><button :disabled="loading || page * 10 >= total" @click="page++; load()">下一页</button></nav>
    <ModalDialog :open="editorOpen" :title="editing ? '编辑知识条目' : '新建知识条目'" :busy="busy" @close="editorOpen = false">
      <p v-if="formError" role="alert" class="error">{{ formError }}</p><form class="editor-form" @submit.prevent="save">
        <label for="entry-title">标题</label><div class="title-input"><input id="entry-title" v-model="title" required maxlength="200" /><span v-if="extension">{{ extension }}</span></div>
        <template v-if="!editing"><label for="entry-content">内容（Markdown）</label><textarea id="entry-content" v-model="content" rows="10" required /><div class="preview-box" aria-label="内容预览"><MarkdownContent :content="content || '输入内容后显示预览。'" /></div></template>
        <div class="actions"><button type="button" :disabled="busy" @click="editorOpen = false">取消</button><button class="primary" :disabled="busy">保存</button></div>
      </form>
    </ModalDialog>
    <ModalDialog :open="!!preview" title="预览知识条目" @close="preview = null"><h3>{{ preview?.title }}</h3><MarkdownContent :content="preview?.content || ''" /></ModalDialog>
    <ModalDialog :open="uploadOpen" title="上传文件" :busy="uploading" @close="uploadOpen = false"><FileUpload v-if="uploadOpen" :key="kbId" :kb-id="kbId" @busy="uploading = $event" @uploaded="page = 1; search = ''; status = ''; load()" /></ModalDialog>
    <ModalDialog :open="!!detail" title="条目详情" @close="detail = null"><h3>{{ detail?.title }}</h3><p>源文件：{{ detail?.source_file_name || '手动录入' }}</p><p v-if="detail?.processing_message" class="error">{{ detail.processing_message }}</p><p v-if="detail?.type === 'FILE' && detail.processing_status === 'FAILED'">处理失败原因解决后，请先删除此失败条目，再重新上传原文件。</p><MarkdownContent :content="detail?.content || ''" /></ModalDialog>
    <ModalDialog :open="deleteOpen" title="删除条目" :busy="busy" @close="deleteOpen = false"><p>确定删除 {{ deleting.length }} 个条目？此操作无法撤销。</p><p v-if="formError" role="alert" class="error">{{ formError }}</p><div class="actions"><button :disabled="busy" @click="deleteOpen = false">取消</button><button class="danger" :disabled="busy" @click="remove">确认删除</button></div></ModalDialog>
  </section>
</template>
