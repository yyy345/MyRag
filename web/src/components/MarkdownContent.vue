<script setup lang="ts">
import { computed } from 'vue';
import MarkdownIt from 'markdown-it';
const props = defineProps<{ content: string }>();
const parser = new MarkdownIt({ html: false, linkify: true, breaks: true });
const html = computed(() => parser.render(props.content));
</script>
<template><div class="markdown-content" v-html="html" /></template>
