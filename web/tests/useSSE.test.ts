import { mount } from '@vue/test-utils';
import { defineComponent } from 'vue';
import { expect, it } from 'vitest';
import { useSSE } from '../src/composables/useSSE';
import type { SSEChunk } from 'shared';

function setup() {
  let state!: ReturnType<typeof useSSE>;
  const wrapper = mount(defineComponent({ setup() { state = useSSE(); return () => ''; } }));
  return { state, wrapper };
}
it('isolates an old stream from a new one even if the source ignores abort', async () => {
  const { state, wrapper } = setup();
  let release!: () => void; const held = new Promise<void>(resolve => { release = resolve; });
  let oldSignal!: AbortSignal;
  const old = state.start(async function* (signal) { oldSignal = signal; await held; yield { type: 'chunk', content: '旧回答', is_end: false }; });
  const current = await state.start(async function* () { yield { type: 'chunk', content: '新回答', is_end: false }; yield { type: 'done', is_end: true }; });
  expect(current).toBe(true); expect(oldSignal.aborted).toBe(true);
  release(); expect(await old).toBe(false);
  expect(state.content.value).toBe('新回答'); expect(state.error.value).toBe(''); expect(state.streaming.value).toBe(false);
  wrapper.unmount();
});
it('distinguishes legacy done errors and truncated answers from success', async () => {
  const { state, wrapper } = setup();
  for (const terminal of [{ type: 'done', is_end: true, content: '生成回答时出错: 测试' } as SSEChunk, null]) {
    expect(await state.start(async function* () { yield { type: 'chunk', content: '未完成回答', is_end: false }; if (terminal) yield terminal; })).toBe(false);
    expect(state.error.value).toBeTruthy(); expect(state.content.value).toBe('未完成回答');
  }
  wrapper.unmount();
});
it('aborts the active source on component unmount and ignores late output', async () => {
  const { state, wrapper } = setup(); let signal!: AbortSignal; let release!: () => void;
  const held = new Promise<void>(resolve => { release = resolve; });
  const running = state.start(async function* (value) { signal = value; await held; yield { type: 'chunk', content: 'late', is_end: false }; });
  wrapper.unmount(); expect(signal.aborted).toBe(true); release();
  expect(await running).toBe(false); expect(state.content.value).toBe('');
});
