import { mount } from '@vue/test-utils';
import { expect, it, vi } from 'vitest';
const chart = vi.hoisted(() => ({ setOption: vi.fn(), resize: vi.fn(), dispose: vi.fn() }));
vi.mock('echarts/core', () => ({ use: vi.fn(), init: () => chart }));
import AnalyticsChart from '../src/components/AnalyticsChart.vue';
it('updates chart data and disposes its resize observer and instance on unmount', async () => {
  const disconnect = vi.fn(); let resize!: () => void;
  vi.stubGlobal('ResizeObserver', class { constructor(callback: () => void) { resize = callback; } observe = vi.fn(); disconnect = disconnect; });
  const wrapper = mount(AnalyticsChart, { props: { label: '统计', option: { series: [] } } });
  try {
    await wrapper.setProps({ option: { series: [{ type: 'bar', data: [7] }] } });
    expect(chart.setOption).toHaveBeenLastCalledWith({ series: [{ type: 'bar', data: [7] }] }, true);
    resize(); expect(chart.resize).toHaveBeenCalledOnce();
    wrapper.unmount(); expect(disconnect).toHaveBeenCalledOnce(); expect(chart.dispose).toHaveBeenCalledOnce();
  } finally { vi.unstubAllGlobals(); }
});
