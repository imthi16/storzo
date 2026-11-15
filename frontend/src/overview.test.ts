import { describe, expect, it } from 'vitest';
import { TYPE_COLORS, orderTypeStats } from './constants';
import type { ChartType } from './constants';
import { S3_UPLOAD_NOTE, hasUploadSourceSplit, uploadActivityText, toUploadDays, uploadCount, uploadDayTitle, uploadSourcesLabel, uploadTotals } from './components/UploadSpark';
import type { UploadDay } from './components/UploadSpark';

const chartTypes: ChartType[] = ['image', 'video', 'audio', 'document', 'archive', 'other'];

describe('file-type palette', () => {
  it('provides a hex color for every ChartType', () => {
    expect(Object.keys(TYPE_COLORS).sort()).toEqual([...chartTypes].sort());
    for (const type of chartTypes) {
      expect(TYPE_COLORS[type]).toMatch(/^#[0-9a-f]{6}$/i);
    }
  });

  it('keeps the validated category order without changing input counts or row order', () => {
    const rows = [...chartTypes].reverse().map((type, i) => ({type, count: i + 1, bytes: (i + 1) * 100}));
    const original = rows.map(row => ({...row}));
    expect(orderTypeStats(rows).map(row => row.type)).toEqual(chartTypes);
    expect(orderTypeStats(rows).map(row => row.count)).toEqual([6, 5, 4, 3, 2, 1]);
    expect(rows).toEqual(original);
    expect(orderTypeStats([])).toEqual([]);
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
    ])).toEqual({total: 10, console: undefined, gateway: undefined, empty: false});
  });

  it('does not present incomplete mixed-source subtotals as complete totals', () => {
    expect(uploadTotals([
      {label: 'New', count: 4, console: 1, gateway: 3},
      {label: 'Legacy', count: 5},
    ])).toEqual({total: 9, console: undefined, gateway: undefined, empty: false});
  });

  it('identifies empty windows, including a full 14 days of source-aware zeros', () => {
    expect(uploadTotals([])).toEqual({total: 0, console: undefined, gateway: undefined, empty: true});
    expect(uploadTotals(Array.from({length: 14}, (_, i) => ({
      label: `Day ${i + 1}`, count: 0, console: 0, gateway: 0,
    })))).toEqual({total: 0, console: 0, gateway: 0, empty: true});
    expect(uploadTotals([{label: 'Legacy zero', count: 0}]).empty).toBe(true);
  });

  it('keeps the chart nonempty for S3 API-only uploads or nonzero legacy data', () => {
    expect(uploadTotals([{label: 'S3 API', count: 0, console: 0, gateway: 3}]))
      .toEqual({total: 3, console: 0, gateway: 3, empty: false});
    expect(uploadTotals([{label: 'Legacy', count: 1}]).empty).toBe(false);
  });
});

describe('upload day descriptions', () => {
  it('labels all gateway PUTs as S3 API and documents multipart counting', () => {
    expect(uploadSourcesLabel([{label: 'Day', count: 58, console: 0, gateway: 58}])).toBe('Console 0 · S3 API 58');
    expect(uploadSourcesLabel([{label: 'Day', count: 2, console: 2, gateway: 0}])).toBe('Console 2 · S3 API 0');
    expect(uploadSourcesLabel([{label: 'Day', count: 7, gateway: 58}])).toBe('Console uploads: 7');
    expect(S3_UPLOAD_NOTE).toBe('Successful S3 PUT requests through the gateway; a multipart upload counts once per part.');
  });

  it('includes each available source, including zero, without inventing missing sources', () => {
    expect(uploadDayTitle({label: 'Nov 15', count: 3, console: 1, gateway: 2}))
      .toBe('Nov 15: 3 uploads · Console: 1 · S3 API: 2 · ' + S3_UPLOAD_NOTE);
    expect(uploadDayTitle({label: 'Nov 15', count: 1, console: 0, gateway: 1}))
      .toBe('Nov 15: 1 upload · Console: 0 · S3 API: 1 · ' + S3_UPLOAD_NOTE);
    expect(uploadDayTitle({label: 'Nov 15', count: 4})).toBe('Nov 15: 4 console uploads');
    expect(uploadDayTitle({label: 'Nov 15', count: 4, gateway: 2}))
      .toBe('Nov 15: 4 console uploads');
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


describe('response-level upload coverage', () => {
  it('requires both numeric source fields on every item, including zero', () => {
    expect(hasUploadSourceSplit([{label: 'Split', count: 0, console: 0, gateway: 0}])).toBe(true);
    expect(hasUploadSourceSplit([{label: 'Legacy', count: 2}])).toBe(false);
    expect(hasUploadSourceSplit([
      {label: 'Split', count: 3, console: 1, gateway: 2},
      {label: 'Partial', count: 2, console: 2},
    ])).toBe(false);
    expect(hasUploadSourceSplit([{label: 'Invalid', count: 1, console: 0, gateway: NaN}])).toBe(false);
    expect(hasUploadSourceSplit([{label: 'Invalid', count: 1, console: Infinity, gateway: 0}])).toBe(false);
    expect(hasUploadSourceSplit([])).toBe(false);
  });

  it('uses legacy counts and console-only text for the entire partially missing response', () => {
    const days: UploadDay[] = [
      {label: 'Complete', count: 2, console: 2, gateway: 9},
      {label: 'Partial', count: 3, gateway: 8},
    ];
    const split = hasUploadSourceSplit(days);
    expect(uploadTotals(days)).toEqual({total: 5, console: undefined, gateway: undefined, empty: false});
    expect(uploadSourcesLabel(days)).toBe('Console uploads: 5');
    expect(days.map(day => uploadDayTitle(day, split)))
      .toEqual(['Complete: 2 console uploads', 'Partial: 3 console uploads']);
    expect(uploadActivityText(days)).toEqual({
      unit: 'console upload',
      emptyMessage: 'No console uploads in the last 14 days.',
      hint: 'Counts console uploads only.',
      note: 'Console uploads.',
      ariaLabel: '5 console uploads in the last 14 days. Counts console uploads only.',
    });
    expect(JSON.stringify(uploadActivityText(days))).not.toContain('S3');
  });

  it('keeps legacy nonempty responses console-only in the footer, tooltips and accessible description', () => {
    const days: UploadDay[] = [{label: 'Legacy', count: 7}];
    expect(uploadSourcesLabel(days)).toBe('Console uploads: 7');
    expect(uploadDayTitle(days[0]!)).toBe('Legacy: 7 console uploads');
    expect(uploadActivityText(days).ariaLabel).toBe('7 console uploads in the last 14 days. Counts console uploads only.');
    expect(JSON.stringify(uploadActivityText(days))).not.toContain('gateway');
  });

  it('shows source-aware wording for complete split responses', () => {
    const days: UploadDay[] = [{label: 'Split', count: 58, console: 0, gateway: 58}];
    expect(uploadSourcesLabel(days)).toBe('Console 0 · S3 API 58');
    expect(uploadDayTitle(days[0]!, hasUploadSourceSplit(days))).toContain(S3_UPLOAD_NOTE);
    expect(uploadActivityText(days)).toEqual({
      unit: 'upload',
      emptyMessage: 'No uploads in the last 14 days.',
      hint: 'Counts console uploads and S3 API PUT requests.',
      note: S3_UPLOAD_NOTE,
      ariaLabel: '58 uploads in the last 14 days. Counts console uploads and S3 API PUT requests.',
    });
  });

  it('uses console-only empty wording without source fields and split wording when coverage is proven', () => {
    for (const days of [[], [{label: 'Legacy zero', count: 0}]]) {
      expect(uploadTotals(days).empty).toBe(true);
      expect(uploadSourcesLabel(days)).toBe('Console uploads: 0');
      const text = uploadActivityText(days);
      expect(text.emptyMessage).toBe('No console uploads in the last 14 days.');
      expect(text.hint).toBe('Counts console uploads only.');
      expect(text.note).toBe('Console uploads.');
      expect(text.ariaLabel).toContain('0 console uploads');
      expect(JSON.stringify(text)).not.toContain('S3');
    }
    const splitDays = [{label: 'Split zero', count: 0, console: 0, gateway: 0}];
    expect(uploadTotals(splitDays).empty).toBe(true);
    expect(uploadActivityText(splitDays).emptyMessage).toBe('No uploads in the last 14 days.');
    expect(uploadActivityText(splitDays).hint).toContain('S3 API');
    expect(uploadActivityText(splitDays).note).toBe(S3_UPLOAD_NOTE);
  });
});
