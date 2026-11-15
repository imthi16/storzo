import type { TypeStat } from './types';

export type ChartType = TypeStat['type'];

// Cool data hues alternate lightness in this stable category order. Validated
// against --rack (#0d1512) with adjacent pairs; labels/counts remain essential.
export const TYPE_COLORS: Record<ChartType, string> = {
  image: '#32a188',
  video: '#2d6ca8',
  audio: '#25a4b1',
  document: '#5d60a9',
  archive: '#619d64',
  other: '#7d549a',
};

export function orderTypeStats(data: TypeStat[]): TypeStat[] {
  const order = Object.keys(TYPE_COLORS) as ChartType[];
  return [...data].sort((a, b) => order.indexOf(a.type) - order.indexOf(b.type));
}

export const PAGE_SIZES = [5, 10, 20, 50];
