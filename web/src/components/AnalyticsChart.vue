<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { init, use, type EChartsCoreOption, type EChartsType } from 'echarts/core';
import { BarChart, PieChart } from 'echarts/charts';
import { GridComponent, TooltipComponent, AriaComponent } from 'echarts/components';
import { SVGRenderer } from 'echarts/renderers';
use([BarChart, PieChart, GridComponent, TooltipComponent, AriaComponent, SVGRenderer]);
const props = defineProps<{ option: EChartsCoreOption; label: string }>();
const element = ref<HTMLDivElement>(); let chart: EChartsType | undefined; let observer: ResizeObserver | undefined;
onMounted(() => {
  if (!element.value) return;
  chart = init(element.value, undefined, { renderer: 'svg' }); chart.setOption(props.option);
  observer = new ResizeObserver(() => chart?.resize()); observer.observe(element.value);
});
watch(() => props.option, option => chart?.setOption(option, true), { deep: true });
onBeforeUnmount(() => { observer?.disconnect(); chart?.dispose(); chart = undefined; });
</script>
<template><div ref="element" class="analytics-chart" role="img" :aria-label="label" /></template>
