import { plural } from '../lib/format';

/** 14-day bar sparkline of upload counts. The caption below is the text alternative. */
export function UploadSpark({days}: {days: {label: string; count: number}[]}) {
  const max = Math.max(1, ...days.map(d => d.count));
  const total = days.reduce((a, d) => a + d.count, 0);
  const peak = days.reduce((a, d) => (d.count > a.count ? d : a), days[0]);
  return <div>
    <div className="spark-wrap"><span className="spark-max" aria-hidden="true">{max}</span><div className="spark" role="img" aria-label={`Upload activity: ${plural(total, 'upload')} in the last 14 days${peak && peak.count > 0 ? `, most on ${peak.label}` : ''}.`}>
      {days.map(d => <div key={d.label} className="spark-bar" title={`${d.label}: ${plural(d.count, 'upload')}`}
        style={{height: `${d.count > 0 ? Math.max(8, Math.round(d.count / max * 100)) : 3}%`}} />)}
    </div></div>
    <p className="muted small spark-cap">{plural(total, 'upload')} in the last 14 days{peak && peak.count > 0 ? ` · busiest ${peak.label}` : ''}</p>
  </div>;
}
