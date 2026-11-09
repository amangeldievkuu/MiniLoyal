import { describe, expect, it } from 'vitest';

import { MiniLoyal } from '../src/index';

describe('MiniLoyal.init', () => {
  it('requires appId and liffId', () => {
    expect(() => MiniLoyal.init({ appId: '', liffId: 'x' })).toThrow(/appId/);
    expect(() => MiniLoyal.init({ appId: 'app_1', liffId: '' })).toThrow(/liffId/);
  });

  it('normalises the base URL', () => {
    const ml = MiniLoyal.init({ appId: 'app_1', liffId: '123-abc', baseUrl: 'https://x.test/' });
    expect(ml.baseUrl).toBe('https://x.test');
  });
});
