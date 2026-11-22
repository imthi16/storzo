import { useEffect, useState, type FormEvent } from 'react';
import { Copy, KeyRound } from 'lucide-react';
import type { ActivityPage, Bucket } from '../types';
import { ApiError, call } from '../api/client';
import { CREDENTIAL_TTL_PRESETS, auditListQuery, credentialEnvText, credentialMcpJson, credentialSigningUnavailable, credentialStatus, enc, msUntilCredentialExpiry, parseIssueDetail, prefixError, ttlPresetEnabled, when, type IssuedDetail } from '../lib/format';
import '../credentials.css';

type IssuedRow = IssuedDetail & {id: number; ts: string; bucket: string | null; prefix: string | null};
type Fresh = {
  access_key_id: string; secret_access_key: string; session_token: string; expires_at: string; endpoint: string;
  bucket: string; prefix: string; mode: 'ro' | 'rw';
};

type Props = {
  csrf: string;
  askConfirm: (title: string, message: string, confirmLabel: string) => Promise<boolean>;
  show: (m: string) => void;
  fail: (e: unknown) => void;
  reloadNonce: number;
};

const REVOKE_UNAVAILABLE = 'revoke is not available on this server yet';

export function CredentialsPage({csrf, askConfirm, show, fail, reloadNonce}: Props) {
  const [buckets, setBuckets] = useState<Bucket[]>([]);
  const [rows, setRows] = useState<IssuedRow[]>([]);
  const [auditTotal, setAuditTotal] = useState(0);
  const [bucket, setBucket] = useState('');
  const [prefix, setPrefix] = useState('');
  const [mode, setMode] = useState<'ro' | 'rw'>('ro');
  const [ttl, setTtl] = useState(900);
  const [label, setLabel] = useState('');
  const [tried, setTried] = useState(false);
  const [issuing, setIssuing] = useState(false);
  const [fresh, setFresh] = useState<Fresh | null>(null);
  const [copied, setCopied] = useState('');
  const [revoked, setRevoked] = useState<string[]>([]);
  const [revoking, setRevoking] = useState('');
  const [localReload, setLocalReload] = useState(0);
  const [nowMs, setNowMs] = useState(() => Date.now());

  useEffect(() => {
    let live = true;
    const q = auditListQuery({limit: 200, offset: 0, source: 'console', decision: 'all'});
    Promise.all([
      call<{items: Bucket[]}>('/buckets'),
      call<ActivityPage>(`/audit?${q}`),
    ]).then(([b, a]) => {
      if (!live) return;
      setBuckets(b.items);
      setAuditTotal(a.total);
      setRows(a.items.flatMap(row => {
        if (row.action !== 'credential.issue') return [];
        const parsed = parseIssueDetail(row.detail);
        if (!parsed) return [];
        return [{...parsed, id: row.id, ts: row.ts, bucket: row.bucket, prefix: row.object_key}];
      }));
    }).catch(e => { if (live) fail(e); });
    return () => { live = false; };
  }, [reloadNonce, localReload]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    const wait = msUntilCredentialExpiry(rows.filter(row => !revoked.includes(row.kid)).map(row => row.exp), nowMs);
    if (wait == null) return;
    const id = window.setTimeout(() => setNowMs(Date.now()), wait);
    return () => window.clearTimeout(id);
  }, [rows, revoked, nowMs]);

  const prefixProblem = (tried || prefix) ? prefixError(prefix) : '';
  const copy = async (id: string, value: string, labelText: string) => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(id);
      window.setTimeout(() => setCopied(cur => cur === id ? '' : cur), 2000);
      show(`${labelText} copied`);
    } catch {
      show('Copy failed. Select the text and copy it manually.');
    }
  };

  const issue = async (e: FormEvent) => {
    e.preventDefault();
    setTried(true);
    if (!bucket || prefixError(prefix) || issuing) return;
    setIssuing(true);
    const body: Record<string, unknown> = {bucket, prefix, mode, ttl_seconds: ttl};
    const trimmed = label.trim();
    if (trimmed) body.label = trimmed;
    try {
      const cred = await call<Omit<Fresh, 'bucket' | 'prefix' | 'mode'>>('/credentials', csrf, {
        method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(body),
      });
      setFresh({...cred, bucket, prefix, mode});
      setTried(false);
      setLocalReload(n => n + 1);
      show('Credential issued. Copy the secret now; it will not be shown again.');
    } catch (err) {
      if (err instanceof ApiError && credentialSigningUnavailable(err.status, err.detail)) show('Agent credentials are not configured on this server (CRED_SIGNING_KEY).');
      else fail(err);
    } finally { setIssuing(false); }
  };

  const revoke = async (kid: string) => {
    if (revoking) return;
    if (!(await askConfirm('Revoke credential', `Revoke ${kid}? This cannot be undone.`, 'Revoke'))) return;
    setRevoking(kid);
    try {
      await call(`/credentials/${enc(kid)}/revoke`, csrf, {method: 'POST'});
      setRevoked(old => old.includes(kid) ? old : [...old, kid]);
      show('Credential revoked');
    } catch (err) {
      if (err instanceof ApiError && (err.status === 404 || err.status === 405)) show(REVOKE_UNAVAILABLE);
      else fail(err);
    } finally { setRevoking(''); }
  };

  const env = fresh ? {
    endpoint: fresh.endpoint, accessKeyId: fresh.access_key_id, secretAccessKey: fresh.secret_access_key,
    sessionToken: fresh.session_token, bucket: fresh.bucket, prefix: fresh.prefix, mode: fresh.mode,
  } : null;

  return <div className="credentials">
    <div className="heading">
      <div>
        <h1>Credentials</h1>
        <p>Issue a short-lived key for one bucket prefix. The gateway enforces the prefix and the read-only or read-write mode. This server accepts a lifetime up to 1 hour.</p>
      </div>
    </div>

    <form className="panel cred-form" onSubmit={issue}>
      <div className="panel-title"><h2>Issue a credential</h2><KeyRound size={16} aria-hidden="true"/></div>
      <div className="cred-grid">
        <label>Bucket
          <select value={bucket} onChange={e => setBucket(e.target.value)} required>
            <option value="">Select a bucket</option>
            {buckets.map(b => <option key={b.name} value={b.name}>{b.name}</option>)}
          </select>
        </label>
        <label>Prefix
          <input value={prefix} onChange={e => setPrefix(e.target.value)} placeholder="agent/" autoComplete="off" spellCheck={false} aria-invalid={!!prefixProblem} aria-describedby={prefixProblem ? 'prefix-error' : undefined}/>
        </label>
        <fieldset>
          <legend>Mode</legend>
          <div className="cred-choice">
            <label><input type="radio" name="cred-mode" value="ro" checked={mode === 'ro'} onChange={() => setMode('ro')}/> Read only</label>
            <label><input type="radio" name="cred-mode" value="rw" checked={mode === 'rw'} onChange={() => setMode('rw')}/> Read and write</label>
          </div>
        </fieldset>
        <fieldset>
          <legend>Lifetime</legend>
          <div className="cred-choice">
            {CREDENTIAL_TTL_PRESETS.map(p => {
              const on = ttlPresetEnabled(p.seconds);
              return <label key={p.seconds} className={on ? '' : 'is-disabled'}>
                <input type="radio" name="cred-ttl" value={p.seconds} checked={ttl === p.seconds} disabled={!on} onChange={() => setTtl(p.seconds)}/>
                {p.label}{on ? '' : ' (over the 1 hour server max)'}
              </label>;
            })}
          </div>
        </fieldset>
        <label>Label <span className="cred-optional">(optional)</span>
          <input value={label} maxLength={100} onChange={e => setLabel(e.target.value)} autoComplete="off" spellCheck={false}/>
        </label>
      </div>
      {prefixProblem && <p id="prefix-error" className="cred-error" role="alert">{prefixProblem}</p>}
      {buckets.length === 0 && <p className="panel-note">Create a bucket first, then come back to issue a key for it.</p>}
      <button className="primary" type="submit" disabled={issuing}>{issuing ? 'Issuing…' : 'Issue credential'}</button>
    </form>

    {fresh && env && <section className="panel cred-once" aria-labelledby="once-title" data-testid="credential-once">
      <div className="panel-title"><h2 id="once-title">Shown once</h2></div>
      <p className="cred-warn" role="status">This secret and session token are shown only this time. They are not stored. Copy them now; they cannot be shown again.</p>
      <dl className="cred-secrets">
        <div><dt>Access key</dt><dd><code data-testid="credential-access-key">{fresh.access_key_id}</code><button type="button" className="secondary compact" onClick={() => copy('ak', fresh.access_key_id, 'Access key')}><Copy size={14} aria-hidden="true"/>{copied === 'ak' ? 'Copied' : 'Copy'}</button></dd></div>
        <div><dt>Secret access key</dt><dd><code data-testid="credential-secret">{fresh.secret_access_key}</code><button type="button" className="secondary compact" onClick={() => copy('sk', fresh.secret_access_key, 'Secret access key')}><Copy size={14} aria-hidden="true"/>{copied === 'sk' ? 'Copied' : 'Copy'}</button></dd></div>
        <div><dt>Session token</dt><dd><code data-testid="credential-token">{fresh.session_token}</code><button type="button" className="secondary compact" onClick={() => copy('tk', fresh.session_token, 'Session token')}><Copy size={14} aria-hidden="true"/>{copied === 'tk' ? 'Copied' : 'Copy'}</button></dd></div>
        <div><dt>Expires</dt><dd><code>{when(fresh.expires_at)}</code></dd></div>
      </dl>
      <h3>storzo-mcp environment</h3>
      <pre data-testid="credential-env">{credentialEnvText(env)}</pre>
      <button type="button" className="secondary compact" onClick={() => copy('env', credentialEnvText(env), 'Environment block')}>{copied === 'env' ? 'Copied' : 'Copy environment'}</button>
      <h3>MCP client config</h3>
      <pre data-testid="credential-mcp">{credentialMcpJson(env)}</pre>
      <button type="button" className="secondary compact" onClick={() => copy('mcp', credentialMcpJson(env), 'MCP config')}>{copied === 'mcp' ? 'Copied' : 'Copy MCP config'}</button>
    </section>}

    <section className="panel" aria-labelledby="issued-title">
      <div className="panel-title"><h2 id="issued-title">Recently issued</h2></div>
      {auditTotal > 200 && <p className="panel-note">This list is the credential issues inside the latest 200 console audit events.</p>}
      {rows.length === 0 ? <p className="cred-empty">No credentials have been issued yet.</p> : <ul className="cred-list">
        {rows.map(row => {
          const status = revoked.includes(row.kid) ? 'revoked' : credentialStatus(row.exp, nowMs / 1000);
          return <li key={row.id}>
            <div>
              <strong className="cred-kid">{row.kid}</strong>
              <small>{row.mode === 'ro' ? 'Read only' : 'Read and write'}{row.label ? ` · ${row.label}` : ''} · {row.bucket || '—'}/{row.prefix || ''} · expires {when(new Date(row.exp * 1000).toISOString())}</small>
            </div>
            <span className={`cred-status ${status}`}>{status === 'active' ? 'Active' : status === 'revoked' ? 'Revoked' : 'Expired'}</span>
            {status === 'active' && <button type="button" className="danger compact" disabled={revoking === row.kid} aria-label={`Revoke ${row.kid}`} onClick={() => revoke(row.kid)}>{revoking === row.kid ? 'Revoking…' : 'Revoke'}</button>}
          </li>;
        })}
      </ul>}
    </section>
  </div>;
}
