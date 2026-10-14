<script setup lang="ts">
import { onBeforeUnmount, ref } from 'vue';
import { entryApi } from '../api/knowledge';
const props = defineProps<{ kbId: string }>();
const emit = defineEmits<{ uploaded: []; busy: [value: boolean] }>();
const kind = ref('document'); const files = ref<File[]>([]); const input = ref<HTMLInputElement>();
const busy = ref(false); const error = ref(''); const notice = ref('');
let controller: AbortController | undefined;
function reset() { files.value = []; error.value = ''; notice.value = ''; if (input.value) input.value.value = ''; }
function select(event: Event) { files.value = Array.from((event.target as HTMLInputElement).files || []); error.value = ''; notice.value = ''; }
async function upload() {
  if (busy.value) return;
  error.value = ''; notice.value = '';
  const images = kind.value === 'image';
  if (!files.value.length) { error.value = '请选择文件'; return; }
  if (files.value.length > (images ? 10 : 1)) { error.value = images ? '每次最多上传 10 张图片' : '每次请选择一个文档'; return; }
  for (const file of files.value) {
    if (!(images ? /\.(png|jpe?g)$/i : /\.(pdf|docx|md|txt|csv|xlsx)$/i).test(file.name)) { error.value = '不支持的文件格式'; return; }
    if (file.size > (images ? 5 : 20) * 1024 * 1024) { error.value = images ? '单张图片最大 5MB' : '文档最大 20MB'; return; }
  }
  busy.value = true; emit('busy', true); controller = new AbortController();
  try {
    if (images) await entryApi.uploadImages(props.kbId, files.value, controller.signal);
    else await entryApi.upload(props.kbId, files.value[0]!, controller.signal);
    notice.value = '已提交文件，请在列表查看处理结果。重复文件会关联已有条目。';
    files.value = []; if (input.value) input.value.value = ''; emit('uploaded');
  } catch (err) { if (!controller.signal.aborted) error.value = err instanceof Error ? err.message : '上传失败，请重试'; }
  finally { busy.value = false; emit('busy', false); }
}
onBeforeUnmount(() => controller?.abort());
</script>
<template>
  <form class="editor-form" @submit.prevent="upload">
    <label for="upload-kind">上传类型</label><select id="upload-kind" v-model="kind" :disabled="busy" @change="reset"><option value="document">文档</option><option value="image">图片</option></select>
    <p class="muted">{{ kind === 'image' ? '支持 PNG、JPG，每张不超过 5MB，每次最多 10 张。' : '支持 PDF、DOCX、Markdown、TXT、CSV、XLSX，每次一个，不超过 20MB。' }}</p>
    <label for="upload-file">选择文件</label><input id="upload-file" ref="input" type="file" :disabled="busy" :multiple="kind === 'image'" :accept="kind === 'image' ? '.png,.jpg,.jpeg' : '.pdf,.docx,.md,.txt,.csv,.xlsx'" @change="select" />
    <p v-if="error" role="alert" class="error">{{ error }}</p><p v-if="notice" role="status">{{ notice }}</p>
    <button class="primary" :disabled="busy || !files.length">{{ busy ? '上传中…' : '上传' }}</button>
  </form>
</template>
