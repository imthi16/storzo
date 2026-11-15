import { plural } from '../lib/format';

export type UploadDay = {label: string; count: number; console?: number; gateway?: number};
export type UploadDayItem = Omit<UploadDay, 'label'> & {day: string};

export const S3_UPLOAD_NOTE = 'Successful S3 PUT requests through the gateway; a multipart upload counts once per part.';

export function uploadSourcesLabel(consoleTotal?: number, gatewayTotal?: number): string {
  return consoleTotal != null && gatewayTotal != null
    ? `Console ${consoleTotal} · S3 API ${gatewayTotal}`
    : 'Counts console uploads and S3 API PUT requests.';
}

export function uploadCount(day: UploadDay): number {
  return day.console != null && day.gateway != null ? day.console + day.gateway : day.count;
}

/** Missing source fields are unknown, not zero; aggregate count still works. */
export function uploadTotals(days: UploadDay[]) {
  const total = days.reduce((sum, day) => sum + uploadCount(day), 0);
  return {
    total,
    console: days.every(day => day.console != null) ? days.reduce((sum, day) => sum + (day.console ?? 0), 0) : undefined,
    gateway: days.every(day => day.gateway != null) ? days.reduce((sum, day) => sum + (day.gateway ?? 0), 0) : undefined,
    empty: total === 0,
  };
}

export function uploadDayTitle(day: UploadDay): string {
  return [
    `${day.label}: ${plural(uploadCount(day), 'upload')}`,
    ...(day.console != null ? [`Console: ${day.console}`] : []),
    ...(day.gateway != null ? [`S3 API: ${day.gateway}`, S3_UPLOAD_NOTE] : []),
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
  const max = Math.max(1, ...days.map(uploadCount));
  const {total, console: consoleTotal, gateway: gatewayTotal, empty} = uploadTotals(days);
  const peak = days.reduce<UploadDay | undefined>((a, d) => (!a || uploadCount(d) > uploadCount(a) ? d : a), undefined);
  if (empty) {
    return <div className="upload-empty">
      <p>No uploads in the last 14 days.</p>
      <small title={S3_UPLOAD_NOTE}>Counts console uploads and S3 API PUT requests.</small>
    </div>;
  }
  return <div>
    <div className="spark-wrap"><span className="spark-max" aria-hidden="true">{max}</span><div className="spark" role="img" aria-label={`Uploads: ${plural(total, 'upload')} in the last 14 days${peak ? `, most on ${peak.label}` : ''}. Counts console uploads and S3 API PUT requests.`}>
      {days.map(day => <div key={day.label} className="spark-bar" title={uploadDayTitle(day)}
        style={{height: `${uploadCount(day) > 0 ? Math.max(8, Math.round(uploadCount(day) / max * 100)) : 3}%`}} />)}
    </div></div>
    <p className="muted small spark-cap">{plural(total, 'upload')} in the last 14 days{peak ? ` · busiest ${peak.label}` : ''}</p>
    <p className="muted small upload-sources" title={S3_UPLOAD_NOTE}>{uploadSourcesLabel(consoleTotal, gatewayTotal)}</p>
  </div>;
}
