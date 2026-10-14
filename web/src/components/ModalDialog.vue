<script setup lang="ts">
import { nextTick, ref, watch } from 'vue';
const props = defineProps<{ open: boolean; title: string; busy?: boolean }>();
const emit = defineEmits<{ close: [] }>();
const dialog = ref<HTMLDialogElement>();
watch(() => props.open, async open => {
  await nextTick();
  if (open && !dialog.value?.open) dialog.value?.showModal();
  else if (!open && dialog.value?.open) dialog.value?.close();
}, { immediate: true });
</script>
<template>
  <dialog ref="dialog" :aria-label="title" @cancel.prevent="!busy && emit('close')">
    <div class="dialog-heading"><h2>{{ title }}</h2><button type="button" :disabled="busy" aria-label="关闭弹窗" @click="emit('close')">关闭</button></div>
    <slot />
  </dialog>
</template>
