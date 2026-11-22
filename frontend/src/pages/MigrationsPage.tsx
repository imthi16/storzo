import { useEffect, useRef, useState } from 'react';
import { CloudDownload } from 'lucide-react';
import { bytes } from '../lib/format';
import {
  cancelMigration, estimateMigration, isActive, listMigrations, prefixValidationError, progressPercent,
  startMigration, verifyLabel, type Estimate, type MigrationForm, type MigrationJob,
} from '../api/migrations';
import '../migrations.css';

type Props = {csrf?: string; fail: (e: unknown) => void; show: (m: string) => void};

const empty: MigrationForm = {endpoint: '', region: 'us-east-1', sourceBucket: '', sourcePrefix: '', accessKeyId: '', secretAccessKey: '', destBucket: '', destPrefix: ''};

export function MigrationsPage({csrf, fail, show}: Props) {
  const [form, setForm] = useState<MigrationForm>(empty);
  const [estimate, setEstimate] = useState<Estimate | null>(null);
  const [jobs, setJobs] = useState<MigrationJob[]>([]);
  const [busy, setBusy] = useState(false);
  const set = (k: keyof MigrationForm) => (e: React.ChangeEvent<HTMLInputElement>) => setForm(f => ({...f, [k]: e.target.value}));
  const active = jobs.find(isActive);
  const timer = useRef<number | null>(null);

  const refresh = async () => { const d = await listMigrations(); setJobs(d.items); };
  useEffect(() => { refresh().catch(fail); }, []); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!active) return;
    timer.current = window.setInterval(() => { refresh().catch(() => undefined); }, 2000);
    return () => { if (timer.current) window.clearInterval(timer.current); };
  }, [active?.id, active?.state]); // eslint-disable-line react-hooks/exhaustive-deps

  const doEstimate = async () => {
    if (!csrf) return;
    const prefixError = prefixValidationError(form);
    if (prefixError) { fail(new Error(prefixError)); return; }
    setBusy(true);
    try { setEstimate(await estimateMigration(csrf, form)); } catch (e) { fail(e); } finally { setBusy(false); }
  };
  const doStart = async () => {
    if (!csrf) return;
    const prefixError = prefixValidationError(form);
    if (prefixError) { fail(new Error(prefixError)); return; }
    setBusy(true);
    try { const job = await startMigration(csrf, form); setJobs(j => [job, ...j]); show('Migration started'); } catch (e) { fail(e); } finally { setBusy(false); }
  };
  const doCancel = async (id: string) => {
    if (!csrf) return;
    try { await cancelMigration(csrf, id); await refresh(); show('Migration cancelled'); } catch (e) { fail(e); }
  };

  return <>
    <div className="heading"><div><h1>Cloud connect</h1><p>One-time copy from AWS S3 or an S3-compatible store into an existing Storzo bucket. This is not sync and not continuous; nothing is deleted on the source. GCS and Azure are not supported yet.</p></div></div>
    <section className="panel migrations-panel">
      <h2>Source (AWS S3 / S3-compatible)</h2>
      <div className="mig-grid">
        <label>Endpoint (blank for AWS)<input value={form.endpoint} onChange={set('endpoint')} placeholder="https://s3.example.com" autoComplete="off"/></label>
        <label>Region<input value={form.region} onChange={set('region')} autoComplete="off"/></label>
        <label>Source bucket<input value={form.sourceBucket} onChange={set('sourceBucket')} autoComplete="off"/></label>
        <label>Source prefix<input value={form.sourcePrefix} onChange={set('sourcePrefix')} placeholder="photos/" autoComplete="off"/></label>
        <label>Access key ID<input value={form.accessKeyId} onChange={set('accessKeyId')} autoComplete="off"/></label>
        <label>Secret access key<input type="password" value={form.secretAccessKey} onChange={set('secretAccessKey')} autoComplete="new-password"/></label>
        <label>Destination Storzo bucket (must already exist)<input value={form.destBucket} onChange={set('destBucket')} autoComplete="off"/></label>
        <label>Destination prefix<input value={form.destPrefix} onChange={set('destPrefix')} placeholder="imported/" autoComplete="off"/></label>
      </div>
      <p className="panel-note">Prefixes must be empty or end with <code>/</code> (for example <code>photos/</code>): a prefix is a folder, not a partial name such as <code>logs/2026-</code>. Source credentials are used only for this job, are passed to rclone via its environment, and are never stored or shown again.</p>
      <div className="mig-actions">
        <button className="secondary" onClick={doEstimate} disabled={busy || !csrf}>Estimate</button>
        <button className="primary" onClick={doStart} disabled={busy || !csrf || !!active}><CloudDownload size={16}/> Start one-time copy</button>
      </div>
      {estimate && <div className="mig-estimate" role="status">
        <strong>{estimate.object_count} objects · {bytes(estimate.total_bytes)}</strong>
        <span>Estimated egress: ${estimate.egress_usd.toFixed(2)} at ${estimate.egress_price_per_gb_usd.toFixed(2)}/GB{estimate.partial ? ` (partial scan, capped at ${estimate.scan_limit})` : ''}</span>
        <small>{estimate.price_note}</small>
      </div>}
    </section>
    <section className="panel migrations-panel">
      <h2>Migrations</h2>
      {jobs.length === 0 ? <p className="panel-note">No migrations yet. Only one migration can run at a time.</p> :
        jobs.map(job => <div className="mig-job" key={job.id}>
          <div><strong>{job.source_bucket}/{job.source_prefix || ''} → {job.dest_bucket}/{job.dest_prefix || ''}</strong>
            <small>{job.state} · {job.objects_done}/{job.total_objects || '?'} objects · {bytes(job.bytes_done)}{job.total_bytes ? ` / ${bytes(job.total_bytes)}` : ''} · {progressPercent(job)}%</small></div>
          <div className="mig-bar"><span style={{width: `${progressPercent(job)}%`}}/></div>
          <small>{verifyLabel(job)}</small>
          {job.errors.length > 0 && <small className="mig-errors">{job.errors.slice(0, 3).join(' · ')}</small>}
          {isActive(job) && <button className="secondary compact" onClick={() => doCancel(job.id)}>Cancel</button>}
        </div>)}
    </section>
  </>;
}
