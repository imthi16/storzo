import { Activity, ChevronRight, Database, File as FileIcon, HardDrive, Image, ShieldCheck, UploadCloud } from 'lucide-react';
import type { Obj, Stats, Page } from '../types';
import { plural, bytes } from '../lib/format';
import { PAGE_SIZES } from '../constants';
import { Pager } from '../components/Pager';
import { RefRow } from '../components/RefRow';
import { TypeDonut } from '../components/TypeDonut';
import { UploadSpark } from '../components/UploadSpark';

type Props = {
  stats: Stats | null; updatedAgo: number | null; uploadDays: {label: string; count: number}[]; ready: 'checking' | 'online' | 'offline';
  barPage: number; setBarPage: (n: number) => void; pageSize: number; changePageSize: (n: number) => void;
  openBucket: (name: string) => void; openPreview: (obj: Obj, bucket: string) => void; nav: (p: Page) => void;
};

export function OverviewPage({stats, updatedAgo, uploadDays, ready, barPage, setBarPage, pageSize, changePageSize, openBucket, openPreview, nav}: Props) {
  return <>
        <div className="heading"><div><h1>Storage overview</h1><p>All your files in one place, running on your infrastructure.{updatedAgo != null && <span className="muted"> Updated {updatedAgo}s ago · auto-refreshes while visible.</span>}</p></div><button className="primary" onClick={()=>nav('buckets')}><Database size={17}/> Browse files</button></div>
        {stats?.sampled && stats.message ? <div className="sample-note" role="note">{stats.message}</div> : null}
        {!stats ? <div className="stat-grid" aria-label="Loading storage statistics">{[0, 1, 2, 3].map(i => <div className="stat" key={i}><div className="skel skel-line" /><div className="skel skel-num" /><div className="skel skel-line short" /></div>)}</div> : <>
        <div className="stat-grid">
          <div className="stat"><div className="stat-head"><span>Total buckets</span></div><strong>{stats.bucket_count}</strong><small>Independent object namespaces</small></div>
          <div className="stat"><div className="stat-head"><span>Stored objects</span></div><strong>{stats.object_count}{stats.sampled ? ' +' : ''}</strong><small>{stats.sampled ? 'Scan capped at 3,000 objects' : 'S3-compatible objects'}</small></div>
          <div className="stat"><div className="stat-head"><span>Data scanned</span></div><strong>{bytes(stats.total_bytes)}</strong><small>{stats.sampled ? 'Approximate, partial scan' : 'Total visible object size'}</small></div>
          <div className="stat"><div className="stat-head"><span>Image assets</span></div><strong>{stats.image_count}</strong><small>JPG, PNG, WebP, GIF and AVIF</small></div>
        </div>
        <div className="two-col">
          <section className="panel"><div className="panel-title"><h2>Upload activity, last 14 days</h2><Activity size={19}/></div><UploadSpark days={uploadDays} /></section>
          <section className="panel system-panel"><div className="panel-title"><h2>System health</h2><ShieldCheck size={19}/></div><div className="health-item"><span>Object storage (S3)</span><strong className={ready==='online'?'good':'bad'}><i className={`led ${ready==='online'?'online':ready==='offline'?'offline':''}`}/>{ready==='online'?'Connected':ready==='offline'?'Unreachable':'Checking'}</strong></div><p className="panel-note">Single-node demo setup: arrange separate backups before putting critical data here.</p></section>
        </div>
        <div className="two-col">
          <section className="panel"><div className="panel-title"><h2>Storage by bucket</h2><Database size={19}/></div>
            {!stats.buckets ? <div className="empty slim"><Database size={24}/><p>Per-bucket breakdown will appear here once the backend reports it.</p></div>
            : stats.buckets.length === 0 ? <div className="empty slim"><Database size={24}/><p>No buckets yet.</p></div>
            : <ul className="bar-list" aria-label="Storage used per bucket">{(() => {
                const rows = stats.buckets ?? [];
                const max = Math.max(1, ...rows.map(b => b.total_bytes));
                return rows.slice(barPage * pageSize, (barPage + 1) * pageSize).map(b => <li key={b.name}><button className="bar-row" onClick={() => openBucket(b.name)} aria-label={`Open bucket ${b.name}, ${b.object_count} objects, ${bytes(b.total_bytes)}`}>
                  <span className="bar-meta"><strong>{b.name}</strong><small>{plural(b.object_count, 'object')} · {bytes(b.total_bytes)}</small></span>
                  <span className="bar-track"><span className="bar-fill" style={{width: `${Math.max(2, Math.round(b.total_bytes / max * 100))}%`}} /></span>
                </button></li>);
              })()}</ul>}
            {stats.buckets && stats.buckets.length > (PAGE_SIZES[0] ?? 0) && <Pager pageNumber={barPage+1} pageSize={pageSize} onSize={changePageSize} hasPrev={barPage>0} hasNext={(barPage+1)*pageSize<stats.buckets.length} onPrev={()=>setBarPage(barPage-1)} onNext={()=>setBarPage(barPage+1)} summary={`Showing ${barPage*pageSize+1}-${Math.min((barPage+1)*pageSize, stats.buckets.length)} of ${stats.buckets.length}`} />}
          </section>
          <section className="panel"><div className="panel-title"><h2>File types</h2><Image size={19}/></div>
            {!stats.by_type ? <div className="empty slim"><FileIcon size={24}/><p>File-type breakdown will appear here once the backend reports it.</p></div>
            : stats.by_type.length === 0 ? <div className="empty slim"><FileIcon size={24}/><p>No files stored yet.</p></div>
            : <TypeDonut data={stats.by_type} />}
          </section>
        </div>
        <div className="two-col">
          <section className="panel"><div className="panel-title"><h2>Recent uploads</h2><button className="text-btn" onClick={() => nav('activity')}>Activity <ChevronRight size={15}/></button></div>
            {!stats.recent ? <div className="empty slim"><UploadCloud size={24}/><p>Recent uploads will appear here once the backend reports them.</p></div>
            : stats.recent.length === 0 ? <div className="empty slim"><UploadCloud size={24}/><p>No uploads yet.</p></div>
            : <ul className="ref-list">{stats.recent.slice(0, 5).map(r => <RefRow key={`${r.bucket}/${r.key}`} r={r} onOpenBucket={openBucket} onPreview={x => openPreview({key: x.key, size: x.size, modified: x.modified, etag: ''}, x.bucket)} />)}</ul>}
          </section>
          <section className="panel"><div className="panel-title"><h2>Largest objects</h2><HardDrive size={19}/></div>
            {!stats.largest ? <div className="empty slim"><HardDrive size={24}/><p>Largest objects will appear here once the backend reports them.</p></div>
            : stats.largest.length === 0 ? <div className="empty slim"><HardDrive size={24}/><p>No objects stored yet.</p></div>
            : <ul className="ref-list">{stats.largest.slice(0, 5).map(r => <RefRow key={`${r.bucket}/${r.key}`} r={r} onOpenBucket={openBucket} onPreview={x => openPreview({key: x.key, size: x.size, modified: x.modified, etag: ''}, x.bucket)} />)}</ul>}
          </section>
        </div>
        </>}
  </>;
}
