<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue';
import { analyticsApi, type Overview } from '../api/analytics';
import AnalyticsChart from '../components/AnalyticsChart.vue';
const data = ref<Overview | null>(null); const loading = ref(false); const error = ref('');
let controller: AbortController | undefined; let timer: ReturnType<typeof setTimeout> | undefined; let disposed = false;
async function load() {
  clearTimeout(timer); controller?.abort(); const current = new AbortController(); controller = current;
  loading.value = true; error.value = '';
  try { const result = await analyticsApi.overview(current.signal); if (!current.signal.aborted) data.value = result; }
  catch (err) { if (!current.signal.aborted) error.value = err instanceof Error ? err.message : '统计加载失败'; }
  finally { if (controller === current) { loading.value = false; if (!disposed) timer = setTimeout(load, 300000); } }
}
const bar = computed(() => ({
  animation: false, aria: { enabled: true }, tooltip: { trigger: 'axis', renderMode: 'richText', confine: true },
  grid: { left: 8, right: 32, top: 12, bottom: 16, containLabel: true },
  xAxis: { type: 'value', minInterval: 1 }, yAxis: { type: 'category', inverse: true, data: data.value?.hotEntries.map(e => e.entry_title) || [], axisLabel: { width: 120, overflow: 'truncate' } },
  series: [{ type: 'bar', name: '检索命中次数', data: data.value?.hotEntries.map(e => e.total_hits) || [], itemStyle: { color: '#946c09', borderRadius: [0, 5, 5, 0] }, label: { show: true, position: 'right' } }],
}));
const active = computed(() => data.value?.kbActivity.filter(kb => kb.total_hits > 0) || []);
const pie = computed(() => ({
  animation: false, aria: { enabled: true }, tooltip: { trigger: 'item', renderMode: 'richText', confine: true },
  color: ['#946c09', '#d4a017', '#c08020', '#5a788a', '#e0c870'],
  series: [{ type: 'pie', radius: ['40%', '68%'], label: { overflow: 'truncate', width: 80 }, data: active.value.map(kb => ({ name: kb.kb_name, value: kb.total_hits })) }],
}));
void load();
onBeforeUnmount(() => { disposed = true; controller?.abort(); clearTimeout(timer); });
</script>
<template>
  <section class="page">
    <div class="page-heading"><div><h1>数据统计</h1><p class="muted">知识检索热度概览，每 5 分钟自动刷新；统计缓存最长 5 分钟。</p></div><button :disabled="loading" @click="load">刷新统计</button></div>
    <p v-if="loading" role="status">正在加载统计…</p><p v-if="error" class="error" role="alert">{{ error }} <button @click="load">重试</button></p>
    <template v-if="data">
      <dl class="summary-grid"><div><dt>知识库</dt><dd>{{ data.summary.kbCount }}</dd></div><div><dt>知识条目</dt><dd>{{ data.summary.entryCount }}</dd></div><div><dt>对话会话</dt><dd>{{ data.summary.sessionCount }}</dd></div></dl>
      <div class="charts-grid"><section class="chart-card"><h2>热门条目 TOP 10</h2><AnalyticsChart v-if="data.hotEntries.length" :option="bar" label="热门条目检索命中柱状图" /><p v-else class="empty">暂无检索命中记录。</p></section><section class="chart-card"><h2>知识库活跃度</h2><AnalyticsChart v-if="active.length" :option="pie" label="知识库检索命中分布图" /><p v-else class="empty">暂无活跃度数据。</p></section></div>
      <div class="table-scroll" v-if="data.hotEntries.length"><table><caption>热门条目明细</caption><thead><tr><th>条目</th><th>知识库</th><th>检索命中</th></tr></thead><tbody><tr v-for="(entry, index) in data.hotEntries" :key="index"><td>{{ entry.entry_title }}</td><td>{{ entry.kb_name }}</td><td>{{ entry.total_hits }}</td></tr></tbody></table></div>
      <div class="table-scroll" v-if="data.kbActivity.length"><table><caption>知识库活跃度明细</caption><thead><tr><th>知识库</th><th>条目数</th><th>检索命中</th></tr></thead><tbody><tr v-for="kb in data.kbActivity" :key="kb.kb_id"><td>{{ kb.kb_name }}</td><td>{{ kb.entry_count }}</td><td>{{ kb.total_hits }}</td></tr></tbody></table></div>
    </template>
  </section>
</template>
