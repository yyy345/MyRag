import { onBeforeUnmount, ref } from 'vue';
import type { Reference, SSEChunk, TokenUsage } from 'shared';

export function useSSE() {
  const streaming = ref(false); const content = ref(''); const error = ref(''); const stopped = ref(false);
  const references = ref<Reference[]>([]); const usage = ref<TokenUsage | null>(null);
  let controller: AbortController | undefined;
  function cancel(clear = false) {
    const wasStreaming = streaming.value;
    controller?.abort(); controller = undefined; streaming.value = false;
    stopped.value = !clear && (wasStreaming || !!content.value);
    if (clear) { content.value = ''; error.value = ''; references.value = []; usage.value = null; }
  }
  async function start(source: (signal: AbortSignal) => AsyncIterable<SSEChunk>): Promise<boolean> {
    cancel(true); stopped.value = false;
    const current = new AbortController(); controller = current; streaming.value = true;
    try {
      for await (const event of source(current.signal)) {
        if (current.signal.aborted) return false;
        if (event.type === 'chunk') content.value += event.content || '';
        else {
          // Legacy service encodes generation errors as done.content.
          if (event.content) throw new Error(event.content);
          references.value = event.references || []; usage.value = event.usage || null;
          return true;
        }
      }
      throw new Error('回答连接意外中断，请重试');
    } catch (err) {
      if (!current.signal.aborted) error.value = err instanceof Error ? err.message : '生成失败，请重试';
      return false;
    } finally { if (controller === current) { controller = undefined; streaming.value = false; } }
  }
  onBeforeUnmount(() => cancel(true));
  return { streaming, content, error, stopped, references, usage, start, cancel };
}
