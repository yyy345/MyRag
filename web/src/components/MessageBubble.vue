<script setup lang="ts">
import type { ChatMessage } from 'shared';
import MarkdownContent from './MarkdownContent.vue';
defineProps<{ message: ChatMessage }>();
</script>
<template>
  <div class="message" :class="{ 'user-message': message.role === 'USER' }">
    <p class="message-role">{{ message.role === 'USER' ? '你' : 'AI 助手' }}</p>
    <MarkdownContent :content="message.content" />
    <details v-if="message.references?.length" class="references"><summary>引用资料（{{ message.references.length }}）</summary><div v-for="(reference, index) in message.references" :key="`${reference.id}-${index}`"><h3>{{ reference.title }}</h3><MarkdownContent :content="reference.content" /></div></details>
    <p v-if="message.token_usage" class="muted token-usage">Token 用量（估算）：输入 {{ message.token_usage.prompt_tokens }} · 输出 {{ message.token_usage.completion_tokens }}</p>
  </div>
</template>
