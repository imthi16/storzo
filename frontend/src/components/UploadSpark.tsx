import { plural } from '../lib/format';

export type UploadDay = {label: string; count: number; console?: number; gateway?: number};
export type UploadDayItem = Omit<UploadDay, 'label'> & {day: string};

export const S3_UPLOAD_NOTE = 'Successful S3 PUT requests through the gateway; a multipart upload counts once per part.';

export function hasUploadSourceSplit(days: UploadDay[]): boolean {
  return days.length > 0 && days.every(day =>
    typeof day.console === 'number' && Number.isFinite(day.console) &&
    typeof day.gateway === 'number' && Number.isFinite(day.gateway));
}

export function uploadCount(day: UploadDay, split = hasUploadSourceSplit([day])): number {
  return split ? (day.console ?? 0) + (day.gateway ?? 0) : day.count;
}

/** A partially populated response uses its legacy count consistently throughout. */
export function uploadTotals(days: UploadDay[]) {
  const split = hasUploadSourceSplit(days);
  const total = days.reduce((sum, day) => sum + uploadCount(day, split), 0);
  return {
    total,
    console: split ? days.reduce((sum, day) => sum + (day.console ?? 0), 0) : undefined,
    gateway: split ? days.reduce((sum, day) => sum + (day.gateway ?? 0), 0) : undefined,
    empty: total === 0,
  };
}

export function uploadSourcesLabel(days: UploadDay[]): string {
  const totals = uploadTotals(days);
  return hasUploadSourceSplit(days)
    ? `Console ${totals.console} · S3 API ${totals.gateway}`
    : `Console uploads: ${totals.total}`;
}

/** Keep every text alternative consistent with the whole response's coverage. */
export function uploadActivityText(days: UploadDay[]) {
  const split = hasUploadSourceSplit(days);
  const {total} = uploadTotals(days);
  const unit = split ? 'upload' : 'console upload';
  const hint = split ? 'Counts console uploads and S3 API PUT requests.' : 'Counts console uploads only.';
  return {
    unit,
    emptyMessage: split ? 'No uploads in the last 14 days.' : 'No console uploads in the last 14 days.',
    hint,
    note: split ? S3_UPLOAD_NOTE : 'Console uploads.',
    ariaLabel: `${plural(total, unit)} in the last 14 days. ${hint}`,
  };
}

export function uploadDayTitle(day: UploadDay, split = hasUploadSourceSplit([day])): string {
  if (!split) return `${day.label}: ${plural(day.count, 'console upload')}`;
  return [
    `${day.label}: ${plural(uploadCount(day, split), 'upload')}`,
    `Console: ${day.console}`,
    `S3 API: ${day.gateway}`,
    S3_UPLOAD_NOTE,
  ].join(' · ');
}

export function toUploadDays(items: UploadDayItem[]): UploadDay[] {
  return items.map(({day, ...counts}) => ({
    label: new Date(day + 'T12:00:00Z').toLocaleDateString(undefined, {month: 'short', day: 'numeric', timeZone: 'UTC'}),
    ...counts,
  }));
}

/** 14-day upload counts; labels and caption provide the text alternative. */
export function UploadSpark({days}: {days: UploadDay[]}) {
  const split = hasUploadSourceSplit(days);
  const text = uploadActivityText(days);
  const max = Math.max(1, ...days.map(day => uploadCount(day, split)));
  const {total, empty} = uploadTotals(days);
  const peak = days.reduce<UploadDay | undefined>((a, d) => (!a || uploadCount(d, split) > uploadCount(a, split) ? d : a), undefined);
  if (empty) {
    return <div className="upload-empty">
      <p>{text.emptyMessage}</p>
      <small title={text.note}>{text.hint}</small>
    </div>;
  }
  return <div>
    <div className="spark-wrap"><span className="spark-max" aria-hidden="true">{max}</span><div className="spark" role="img" aria-label={`${text.ariaLabel}${peak ? ` Most on ${peak.label}.` : ''}`}>
      {days.map(day => <div key={day.label} className="spark-bar" title={uploadDayTitle(day, split)}
        style={{height: `${uploadCount(day, split) > 0 ? Math.max(8, Math.round(uploadCount(day, split) / max * 100)) : 3}%`}} />)}
    </div></div>
    <p className="muted small spark-cap">{plural(total, text.unit)} in the last 14 days{peak ? ` · busiest ${peak.label}` : ''}</p>
    <p className="muted small upload-sources" title={text.note}>{uploadSourcesLabel(days)}</p>
  </div>;
}
