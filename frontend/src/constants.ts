import type { TypeStat } from './types';

export type ChartType = TypeStat['type'];

// Validated against --rack (#0d1512), including all-pairs separation so
// missing categories or a different backend order cannot create weak neighbors.
export const TYPE_COLORS: Record<ChartType, string> = {
  image: '#27a68b',
  video: '#1166cc',
  audio: '#dd2255',
  document: '#995599',
  archive: '#227722',
  other: '#bb55ff',
};
export const PAGE_SIZES = [5, 10, 20, 50];
