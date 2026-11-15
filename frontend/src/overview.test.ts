import { describe, expect, it } from 'vitest';
import { TYPE_COLORS } from './constants';
import type { ChartType } from './constants';
import { toUploadDays, uploadCount, uploadDayTitle, uploadTotals } from './components/UploadSpark';
import type { UploadDay } from './components/UploadSpark';

const chartTypes: ChartType[] = ['image', 'video', 'audio', 'document', 'archive', 'other'];

describe('file-type palette', () => {
  it('provides a hex color for every ChartType', () => {
    expect(Object.keys(TYPE_COLORS).sort()).toEqual([...chartTypes].sort());
    for (const type of chartTypes) {
      expect(TYPE_COLORS[type]).toMatch(/^#[0-9a-f]{6}$/i);
    }
  });

  it('assigns six distinct category colors and keeps status colors reserved', () => {
    const colors = chartTypes.map(type => TYPE_COLORS[type].toLowerCase());
    expect(new Set(colors).size).toBe(6);
    for (const reserved of ['#3ddc84', '#f0a63a', '#ef5b5b']) {
      expect(colors).not.toContain(reserved);
    }
  });
});

describe('upload counts', () => {
  it('totals console and gateway uploads across all 14 days', () => {
    const days: UploadDay[] = Array.from({length: 14}, (_, i) => ({
      label: `Day ${i + 1}`, count: 999, console: i, gateway: i * 2,
    }));
    expect(uploadTotals(days)).toEqual({total: 273, console: 91, gateway: 182, empty: false});
  });

  it('uses aggregate count for legacy responses without source fields', () => {
    const days: UploadDay[] = Array.from({length: 14}, (_, i) => ({
      label: `Day ${i + 1}`, count: i,
    }));
    expect(uploadTotals(days)).toEqual({total: 91, console: undefined, gateway: undefined, empty: false});
  });

  it('falls back to count when only one source is available', () => {
    expect(uploadCount({label: 'One', count: 7, console: 2})).toBe(7);
    expect(uploadCount({label: 'Two', count: 7, gateway: 3})).toBe(7);
    expect(uploadTotals([
      {label: 'One', count: 4, console: 1, gateway: 3},
      {label: 'Two', count: 6, console: 2},
    ])).toEqual({total: 10, console: 3, gateway: undefined, empty: false});
  });

  it('does not present incomplete mixed-source subtotals as complete totals', () => {
    expect(uploadTotals([
      {label: 'New', count: 4, console: 1, gateway: 3},
      {label: 'Legacy', count: 5},
    ])).toEqual({total: 9, console: undefined, gateway: undefined, empty: false});
  });

  it('identifies empty windows, including a full 14 days of source-aware zeros', () => {
    expect(uploadTotals([])).toEqual({total: 0, console: 0, gateway: 0, empty: true});
    expect(uploadTotals(Array.from({length: 14}, (_, i) => ({
      label: `Day ${i + 1}`, count: 0, console: 0, gateway: 0,
    })))).toEqual({total: 0, console: 0, gateway: 0, empty: true});
    expect(uploadTotals([{label: 'Legacy zero', count: 0}]).empty).toBe(true);
  });

  it('keeps the chart nonempty for agent-only uploads or nonzero legacy data', () => {
    expect(uploadTotals([{label: 'Agents', count: 0, console: 0, gateway: 3}]))
      .toEqual({total: 3, console: 0, gateway: 3, empty: false});
    expect(uploadTotals([{label: 'Legacy', count: 1}]).empty).toBe(false);
  });
});

describe('upload day descriptions', () => {
  it('includes each available source, including zero, without inventing missing sources', () => {
    expect(uploadDayTitle({label: 'Nov 15', count: 3, console: 1, gateway: 2}))
      .toBe('Nov 15: 3 uploads · console: 1 · agents: 2');
    expect(uploadDayTitle({label: 'Nov 15', count: 1, console: 0, gateway: 1}))
      .toBe('Nov 15: 1 upload · console: 0 · agents: 1');
    expect(uploadDayTitle({label: 'Nov 15', count: 4})).toBe('Nov 15: 4 uploads');
    expect(uploadDayTitle({label: 'Nov 15', count: 4, gateway: 2}))
      .toBe('Nov 15: 4 uploads · agents: 2');
  });

  it('preserves optional API source counts and uses UTC date labels', () => {
    const label = new Date('2026-11-15T12:00:00Z')
      .toLocaleDateString(undefined, {month: 'short', day: 'numeric', timeZone: 'UTC'});
    expect(toUploadDays([
      {day: '2026-11-15', count: 3, console: 1, gateway: 2},
      {day: '2026-11-15', count: 4},
    ])).toEqual([
      {label, count: 3, console: 1, gateway: 2},
      {label, count: 4},
    ]);
    expect(toUploadDays([])).toEqual([]);
  });
});
