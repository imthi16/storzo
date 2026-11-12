import { afterEach, describe, expect, it, vi } from 'vitest';
import { hashFor, pageFromHash } from './routes';

const at = (hash: string) => vi.stubGlobal('location', { hash });
afterEach(() => vi.unstubAllGlobals());

describe('pageFromHash', () => {
  it('falls back to the dashboard for unknown or empty hashes', () => {
    at('');
    expect(pageFromHash()).toEqual({ page: 'dashboard', id: null });
    at('#/nope');
    expect(pageFromHash()).toEqual({ page: 'dashboard', id: null });
  });
  it('parses known pages', () => {
    at('#/buckets');
    expect(pageFromHash()).toEqual({ page: 'buckets', id: null });
  });
  it('parses an activity id and rejects non-numeric ids', () => {
    at('#/activity/12');
    expect(pageFromHash()).toEqual({ page: 'activity-detail', id: 12 });
    at('#/activity/abc');
    expect(pageFromHash()).toEqual({ page: 'activity', id: null });
  });
});

describe('hashFor', () => {
  it('round-trips with pageFromHash', () => {
    for (const [page, id] of [['dashboard', null], ['buckets', null], ['activity-detail', 7]] as const) {
      at(hashFor(page, id));
      expect(pageFromHash()).toEqual({ page, id });
    }
  });
});
