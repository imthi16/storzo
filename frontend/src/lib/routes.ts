import type { Page } from '../types';

export const pageFromHash = (): {page: Page; id: number | null} => {
  const [p, id] = location.hash.replace(/^#\/?/, '').split('/');
  if (p === 'activity' && id && /^\d+$/.test(id)) return {page: 'activity-detail', id: Number(id)};
  const known: Record<string, Page> = {overview: 'dashboard', buckets: 'buckets', activity: 'activity', settings: 'settings'};
  return {page: known[p ?? ''] ?? 'dashboard', id: null};
};
export const hashFor = (p: Page, id: number | null) => p === 'dashboard' ? '#/overview' : p === 'activity-detail' ? (id ? `#/activity/${id}` : '#/activity') : `#/${p}`;
