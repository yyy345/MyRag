import { describe, expect, it } from 'vitest';
import { mount, flushPromises } from '@vue/test-utils';
import App from '../src/App.vue';
import { createRouter, createMemoryHistory } from 'vue-router';
import { routes } from '../src/router';
import { createPinia } from 'pinia';

describe('Vue application', () => {
  it('navigates to library and shows an explicit 404 for unknown routes', async () => {
    const router = createRouter({ history: createMemoryHistory(), routes });
    await router.push('/analytics');
    const wrapper = mount(App, { global: { plugins: [createPinia(), router] } });
    await flushPromises();
    expect(wrapper.get('h1').text()).toBe('数据统计');
    await router.push('/does-not-exist');
    expect(wrapper.get('h1').text()).toBe('页面不存在');
    wrapper.unmount();
  // This also imports the lazy analytics route and chart dependencies.
  }, 15_000);
});
