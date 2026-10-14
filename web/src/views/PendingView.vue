<script setup lang="ts">
import { useRoute } from 'vue-router';
const route = useRoute();
</script>
<template>
  <section class="page"><h1>{{ route.meta.title }}</h1><p>此页面尚未迁移，请暂时使用原版应用。</p></section>
</template>
