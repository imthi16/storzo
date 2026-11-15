import { Activity, ArrowLeft, Database } from 'lucide-react';
import type { Audit } from '../types';
import { activityTitle, bytes, gatewayKidDisplay, parseGatewayDetail, when } from '../lib/format';

export function ActivityDetail({entry, error, onBack, onOpenLocation}: {entry: Audit | null; error: string; onBack: () => void; onOpenLocation: (bucket: string, key: string | null) => void}) {
  if (error) return <section className="panel"><div className="empty"><Activity size={25}/><strong>Cannot show this event</strong><p>{error}</p><button className="secondary compact" onClick={onBack}><ArrowLeft size={15}/> Back to activity</button></div></section>;
  if (!entry) return <section className="panel" aria-label="Loading event"><div className="skel skel-line"/><div className="skel skel-line short"/><div className="skel skel-line"/></section>;
  const gw = entry.action === 'gateway.request' ? parseGatewayDetail(entry.detail) : null;
  const kid = gw ? gatewayKidDisplay(gw.kid) : null;
  const facts = gw ? [] : (entry.detail || '').split(';').map(x => x.trim()).filter(Boolean).map((x): [string, string] => { const i = x.indexOf('='); return i < 0 ? ['note', x] : [x.slice(0, i), x.slice(i + 1)]; });
  const label = (k: string, v: string) => k === 'bytes' && /^\d+$/.test(v) ? `${bytes(Number(v))} (${Number(v).toLocaleString()} bytes)` : v;
  return <>
    <section className="panel detail-panel">
      <dl className="detail-grid">
        <div><dt>Event ID</dt><dd className="mono">#{entry.id}</dd></div>
        <div><dt>Action</dt><dd>{activityTitle(entry.action, entry.detail)}</dd></div>
        <div><dt>When</dt><dd>{when(entry.ts)} <span className="muted mono">{entry.ts}</span></dd></div>
        <div><dt>Actor</dt><dd>{entry.actor}</dd></div>
        <div><dt>Bucket</dt><dd className="mono">{entry.bucket ?? 'none (console event)'}</dd></div>
        <div><dt>Object key</dt><dd className="mono wrap">{entry.object_key ?? 'none'}</dd></div>
      </dl>
    </section>
    {gw && <section className="panel detail-panel"><div className="panel-title"><h2>Gateway request</h2></div>
      <dl className="detail-grid">
        <div><dt>Method</dt><dd className="mono">{gw.method}</dd></div>
        <div><dt>Decision</dt><dd>{gw.decision}</dd></div>
        <div><dt>Status</dt><dd className="mono">{gw.status}</dd></div>
        <div><dt>Bytes</dt><dd className="mono">{bytes(gw.bytes)} ({gw.bytes.toLocaleString()} bytes)</dd></div>
        <div><dt>Kid</dt><dd className="mono wrap" title={kid?.note ?? undefined}>{kid?.value}{kid?.note && <span className="muted"> — {kid.note}</span>}</dd></div>
      </dl>
    </section>}
    {facts.length > 0 && <section className="panel detail-panel"><div className="panel-title"><h2>Details</h2></div>
      <dl className="detail-grid">{facts.map(([k, v]) => <div key={k}><dt>{k.replaceAll('_', ' ')}</dt><dd className="mono wrap">{label(k, v)}</dd></div>)}</dl>
    </section>}
    <div className="detail-actions">
      <button className="secondary" onClick={onBack}><ArrowLeft size={16}/> Back to activity</button>
      {entry.bucket && <button className="primary" onClick={() => onOpenLocation(entry.bucket!, entry.object_key)}><Database size={16}/> {entry.object_key ? 'Open its folder' : 'Open bucket'}</button>}
    </div>
  </>;
}
