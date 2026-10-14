import { expect, it } from 'vitest';
import { mount } from '@vue/test-utils';
import MarkdownContent from '../src/components/MarkdownContent.vue';

it('renders headings and tables while blocking executable HTML and links', () => {
  const wrapper = mount(MarkdownContent, { props: { content: '# 标题\n\n| A | B |\n|---|---|\n| 1 | 2 |\n\n<script>alert(1)</script>\n\n[x](javascript:alert(1))' } });
  expect(wrapper.get('h1').text()).toBe('标题');
  expect(wrapper.findAll('td').map(cell => cell.text())).toEqual(['1', '2']);
  expect(wrapper.find('script').exists()).toBe(false);
  expect(wrapper.find('a[href^="javascript:"]').exists()).toBe(false);
});
