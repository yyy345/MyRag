import { useState, useCallback, useRef } from 'react';
import type { SSEChunk, Reference, TokenUsage } from 'shared';

interface UseSSEReturn {
  streaming: boolean;
  streamContent: string;
  references: Reference[] | null;
  usage: TokenUsage | null;
  startStream: (generator: AsyncGenerator<SSEChunk>) => Promise<string>;
  cancelStream: () => void;
}

/**
 * SSE 流式消费 Hook。
 * 管理流式状态，消费 AsyncGenerator 并累积内容。
 */
export function useSSE(): UseSSEReturn {
  const [streaming, setStreaming] = useState(false);
  const [streamContent, setStreamContent] = useState('');
  const [references, setReferences] = useState<Reference[] | null>(null);
  const [usage, setUsage] = useState<TokenUsage | null>(null);
  const abortRef = useRef(false);

  const startStream = useCallback(
    async (generator: AsyncGenerator<SSEChunk>): Promise<string> => {
      setStreaming(true);
      setStreamContent('');
      setReferences(null);
      setUsage(null);
      abortRef.current = false;
      let full = '';
      let completed = false;

      try {
        for await (const event of generator) {
          if (abortRef.current) break;
          if (!event || completed) throw new Error('回答数据格式异常，请重新发送');
          if (event.type === 'chunk' && typeof event.content === 'string') {
            full += event.content;
            setStreamContent(full);
          } else if (event.type === 'done') {
            // The Go API returns failures as terminal events with content.
            if (event.content) throw new Error(event.content);
            if (!full.trim()) throw new Error('未收到有效回答，请重新发送');
            completed = true;
            setReferences(event.references || null);
            setUsage(event.usage || null);
          } else {
            throw new Error('回答数据格式异常，请重新发送');
          }
        }
        if (abortRef.current) return '';
        if (!completed) throw new Error('回答连接中断，请重新发送');
      } catch (err) {
        setStreamContent('');
        if (abortRef.current) return '';
        throw err;
      } finally {
        setStreaming(false);
      }
      return full;
    },
    [],
  );

  const cancelStream = useCallback(() => {
    abortRef.current = true;
    setStreaming(false);
  }, []);

  return { streaming, streamContent, references, usage, startStream, cancelStream };
}
