import React, { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { createRoot } from 'react-dom/client';
import { Activity, ArrowDownToLine, ArrowLeft, CheckCircle2, ChevronRight, CloudOff, Copy, Database, File as FileIcon, FileImage, Folder, HardDrive, Image, LayoutDashboard, Link, LockKeyhole, LogOut, Menu, Plus, RefreshCw, Search, Server, Settings, ShieldCheck, Trash2, UploadCloud, X } from 'lucide-react';
import './style.css';

type Session = {username: string; csrf: string};
type Bucket = {name: string; created: string};
type Obj = {key: string; size: number; modified: string; etag: string};
type Listing = {items: Obj[]; folders: string[]; continuation: string | null; prefix: string};
type Stats = {bucket_count: number; object_count: number; total_bytes: number; image_count: number; sampled: boolean; message: string};
type Audit = {id: number; ts: string; actor: string; action: string; bucket: string | null; object_key: string | null; detail: string | null};
type Page = 'dashboard' | 'buckets' | 'activity' | 'settings';
const enc = encodeURIComponent;
const bytes = (n: number) => n < 1024 ? `${n} B` : n < 1024 ** 2 ? `${(n/1024).toFixed(1)} KB` : n < 1024 ** 3 ? `${(n/1024**2).toFixed(1)} MB` : `${(n/1024**3).toFixed(2)} GB`;
const isImage = (k: string) => /\.(jpg|jpeg|png|webp|gif|avif)$/i.test(k);
const shortName = (k: string) => k.split('/').filter(Boolean).pop() || k;
const when = (s: string) => s ? new Date(s).toLocaleString() : '—';

async function call<T>(path: string, csrf?: string, options: RequestInit = {}): Promise<T> {
  const method = (options.method || 'GET').toUpperCase();
  const headers = new Headers(options.headers);
  if (csrf && !['GET', 'HEAD', 'OPTIONS'].includes(method)) headers.set('X-CSRF-Token', csrf);
  const res = await fetch('/api' + path, {...options, credentials: 'same-origin', headers});
  if (!res.ok) {
    let error = `Request failed (${res.status})`;
    try { const detail = await res.json(); if (detail.detail) error = detail.detail; } catch { /* ignore */ }
    throw new Error(error);
  }
  return res.json() as Promise<T>;
}

function App() {
  const [session, setSession] = useState<Session | null>(null);
  const [booting, setBooting] = useState(true);
  const [page, setPage] = useState<Page>('dashboard');
  const [menuOpen, setMenuOpen] = useState(false);
  const [username, setUsername] = useState('admin');
  const [password, setPassword] = useState('');
  const [loginBusy, setLoginBusy] = useState(false);
  const [notice, setNotice] = useState('');
  const [buckets, setBuckets] = useState<Bucket[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [prefix, setPrefix] = useState('');
  const [listing, setListing] = useState<{bucket: string; data: Listing} | null>(null);
  const list = listing?.bucket === selected && listing.data.prefix === prefix
    ? listing.data : {items: [], folders: [], continuation: null, prefix};
  const listingRequest = useRef(0);
  const objectLocation = useRef({bucket: selected, prefix});
  useLayoutEffect(() => {
    objectLocation.current = {bucket: selected, prefix};
    listingRequest.current++;
    setListing(null);
    setPreview(null);
  }, [selected, prefix, session]);
  const [stats, setStats] = useState<Stats | null>(null);
  const [audit, setAudit] = useState<Audit[]>([]);
  const [ready, setReady] = useState<'checking'|'online'|'offline'>('checking');
  const [busy, setBusy] = useState(false);
  const [search, setSearch] = useState('');
  const [preview, setPreview] = useState<Obj | null>(null);
  const fileInput = useRef<HTMLInputElement>(null);

  const csrf = session?.csrf;
  const show = (m: string) => { setNotice(m); window.setTimeout(() => setNotice(''), 6000); };
  const fail = (e: unknown) => show(e instanceof Error ? e.message : 'Unexpected error');
  const reloadBuckets = async () => { const d = await call<{items: Bucket[]}>('/buckets'); setBuckets(d.items); };
  const reloadStats = async () => { const d = await call<Stats>('/dashboard'); setStats(d); };
  const reloadAudit = async () => { const d = await call<{items: Audit[]}>('/audit'); setAudit(d.items); };
  const reloadObjects = async (bucket = selected, path = prefix, token = '') => {
    if (!bucket || bucket !== objectLocation.current.bucket || path !== objectLocation.current.prefix) return;
    const request = ++listingRequest.current;
    const p = `/buckets/${enc(bucket)}/objects?prefix=${enc(path)}&limit=100${token ? `&continuation=${enc(token)}` : ''}`;
    try {
      const data = await call<Listing>(p);
      if (request !== listingRequest.current) return;
      setListing(old => ({bucket, data: token && old?.bucket === bucket && old.data.prefix === path
        ? {...data, items: [...old.data.items, ...data.items], folders: [...old.data.folders, ...data.folders]} : data}));
    } catch (error) {
      if (request === listingRequest.current) throw error;
    }
  };
  const refresh = async () => {
    setBusy(true);
    try {
      if (page === 'dashboard') await Promise.all([reloadStats(), reloadBuckets()]);
      if (page === 'buckets') { await reloadBuckets(); if (selected) await reloadObjects(); }
      if (page === 'activity') await reloadAudit();
      const res = await fetch('/api/health/ready'); setReady(res.ok ? 'online' : 'offline');
    } catch(e) { fail(e); } finally { setBusy(false); }
  };

  useEffect(() => { call<Session>('/auth/me').then(setSession).catch(() => setSession(null)).finally(() => setBooting(false)); }, []);
  useEffect(() => { if (!session) return; refresh(); }, [session, page]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { if (!session || page !== 'buckets' || !selected) return; reloadObjects(selected, prefix).catch(fail); }, [selected, prefix]); // eslint-disable-line react-hooks/exhaustive-deps

  const login = async (event: React.FormEvent) => {
    event.preventDefault(); setLoginBusy(true);
    try { const data = await call<Session>('/auth/login', undefined, {method:'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({username, password})}); setSession(data); setPassword(''); }
    catch(e) { fail(e); } finally { setLoginBusy(false); }
  };
  const logout = async () => { try { await call('/auth/logout', csrf, {method:'POST'}); } catch(e) { fail(e); } setSession(null); setSelected(null); };
  const createBucket = async () => {
    const name = window.prompt('New bucket name (lowercase letters, numbers, hyphens):');
    if (!name) return;
    setBusy(true);
    try { await call('/buckets', csrf, {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({name:name.trim()})}); await reloadBuckets(); setSelected(name.trim()); setPrefix(''); setPage('buckets'); show('Bucket created'); }
    catch(e) { fail(e); } finally { setBusy(false); }
  };
  const deleteBucket = async () => {
    if (!selected || !window.confirm(`Delete empty bucket "${selected}"? This cannot be undone.`)) return;
    try { await call(`/buckets/${enc(selected)}`, csrf, {method:'DELETE'}); setSelected(null); setPrefix(''); await reloadBuckets(); show('Bucket removed'); } catch(e) { fail(e); }
  };
  const upload = async (files: FileList | File[] | null) => {
    if (!selected || !files?.length) return;
    setBusy(true);
    let count = 0;
    for (const file of Array.from(files)) {
      const data = new FormData(); data.append('bucket', selected); data.append('key', prefix + file.name); data.append('file', file);
      try { await call('/objects/upload', csrf, {method:'POST', body:data}); count++; } catch(e) { fail(e); }
    }
    if (fileInput.current) fileInput.current.value = '';
    try { await reloadObjects(); await reloadStats(); } catch(e) { fail(e); }
    show(`Uploaded ${count}/${files.length} file(s)`); setBusy(false);
  };
  const deleteFile = async (key: string) => {
    if (!selected || !window.confirm(`Permanently delete "${shortName(key)}"?`)) return;
    try { await call(`/objects?bucket=${enc(selected)}&key=${enc(key)}`, csrf, {method:'DELETE'}); await reloadObjects(); show('Object deleted'); } catch(e) { fail(e); }
  };
  const linkFile = async (key: string) => {
    if (!selected) return;
    try { const res = await call<{url:string}>(`/objects/presign?bucket=${enc(selected)}&key=${enc(key)}&expires=300`); await navigator.clipboard.writeText(res.url); show('5-minute signed URL copied'); }
    catch(e) { fail(e); }
  };
  const nav = (p: Page) => { setPage(p); setMenuOpen(false); };
  const filtered = list.items.filter(o => shortName(o.key).toLowerCase().includes(search.toLowerCase()));
  const openBucket = (name: string) => { setSelected(name); setPrefix(''); setSearch(''); setPage('buckets'); };

  if (booting) return <div className="loading"><Database size={35}/><p>Connecting to Storzo…</p></div>;
  if (!session) return <div className="login-page"><div className="login-card"><div className="brand-icon large"><Database size={26}/></div><div className="eyebrow">SELF-HOSTED OBJECT STORAGE</div><h1>Welcome to <span>Storzo.</span></h1><p>Your images and objects. Your infrastructure. No cloud storage bill.</p><form onSubmit={login}><label>Username<input value={username} onChange={e=>setUsername(e.target.value)} autoComplete="username" required/></label><label>Password<input value={password} onChange={e=>setPassword(e.target.value)} type="password" autoComplete="current-password" required/></label><button className="primary full" disabled={loginBusy}><LockKeyhole size={17}/>{loginBusy?'Signing in…':'Sign in to console'}</button></form><p className="footnote">This console manages the locally hosted SeaweedFS S3 service.</p></div>{notice && <div className="toast">{notice}</div>}</div>;

  return <div className="shell">
    <aside className={`sidebar ${menuOpen?'expanded':''}`}>
      <div className="brand"><div className="brand-icon"><Database size={22}/></div><div><strong>Storzo</strong><small>STORAGE CONSOLE</small></div></div>
      <div className="side-label">WORKSPACE</div>
      <nav>
        <button className={page==='dashboard'?'nav active':'nav'} onClick={()=>nav('dashboard')}><LayoutDashboard size={19}/>Overview</button>
        <button className={page==='buckets'?'nav active':'nav'} onClick={()=>nav('buckets')}><Database size={19}/>Buckets & objects</button>
        <button className={page==='activity'?'nav active':'nav'} onClick={()=>nav('activity')}><Activity size={19}/>Activity log</button>
        <button className={page==='settings'?'nav active':'nav'} onClick={()=>nav('settings')}><Settings size={19}/>Settings</button>
      </nav>
      <div className="side-bottom"><div className="infra-tag"><span className={`pulse ${ready}`}/>{ready==='online'?'Storage connected':ready==='offline'?'Storage unavailable':'Checking storage'}</div><div className="side-user"><div className="avatar">{session.username[0].toUpperCase()}</div><div><strong>{session.username}</strong><small>Administrator</small></div><button onClick={logout} title="Sign out" className="icon-btn"><LogOut size={17}/></button></div></div>
    </aside>
    <main className="content">
      <header className="topbar"><button className="mobile-toggle icon-btn" onClick={()=>setMenuOpen(!menuOpen)}><Menu/></button><div className="breadcrumbs"><span>Workspace</span><ChevronRight size={15}/><strong>{page==='dashboard'?'Overview':page==='buckets'?'Buckets & objects':page==='activity'?'Activity log':'Settings'}</strong></div><div className="top-actions"><div className="local-badge"><CloudOff size={15}/> 100% SELF-HOSTED</div><button className="secondary compact" onClick={refresh} disabled={busy}><RefreshCw size={16} className={busy?'spin':''}/> Refresh</button></div></header>
      <div className="page-body">
      {page==='dashboard' && <>
        <div className="heading"><div><div className="eyebrow">STORAGE OVERVIEW</div><h1>Good to see you, {session.username} <span className="wave">✦</span></h1><p>All your files in one place, running on your infrastructure.</p></div><button className="primary" onClick={()=>nav('buckets')}><Database size={17}/> Browse files</button></div>
        <div className="stat-grid">
          <div className="stat"><div className="stat-head"><span>Total buckets</span><Database size={19}/></div><strong>{stats?.bucket_count ?? '—'}</strong><small>Independent object namespaces</small></div>
          <div className="stat"><div className="stat-head"><span>Stored objects</span><FileIcon size={19}/></div><strong>{stats?.object_count ?? '—'}{stats?.sampled?' +':''}</strong><small>{stats?.sampled?'Scan capped at 3,000 objects':'S3-compatible objects'}</small></div>
          <div className="stat"><div className="stat-head"><span>Data scanned</span><HardDrive size={19}/></div><strong>{stats ? bytes(stats.total_bytes) : '—'}</strong><small>{stats?.sampled?'Approximate, partial scan':'Total visible object size'}</small></div>
          <div className="stat"><div className="stat-head"><span>Image assets</span><Image size={19}/></div><strong>{stats?.image_count ?? '—'}</strong><small>JPG, PNG, WebP, GIF and AVIF</small></div>
        </div>
        <div className="two-col"><section className="panel"><div className="panel-title"><h2>Storage buckets</h2><button className="text-btn" onClick={()=>nav('buckets')}>View all <ChevronRight size={15}/></button></div>{buckets.length===0?<div className="empty"><Database size={28}/><strong>No buckets yet</strong><p>Create your first bucket to store images.</p><button className="primary" onClick={createBucket}><Plus size={16}/> Create bucket</button></div>:<div className="bucket-list">{buckets.slice(0,6).map(b=><button key={b.name} onClick={()=>openBucket(b.name)} className="bucket-row"><div className="row-icon"><Database size={18}/></div><div><strong>{b.name}</strong><small>{when(b.created)}</small></div><ChevronRight size={16}/></button>)}</div>}</section><section className="panel system-panel"><div className="panel-title"><h2>System health</h2><ShieldCheck size={19}/></div><div className="system-art"><Server size={46}/><div className="signal"><span/><span/><span/></div></div><div className="health-item"><span>Object storage</span><strong className={ready==='online'?'good':'bad'}>{ready==='online'?'Connected':'Check connection'}</strong></div><div className="health-item"><span>Deployment</span><strong>Local / Kubernetes</strong></div><div className="health-item"><span>Cloud required</span><strong>No</strong></div><p className="panel-note">Single-node demo setup: arrange separate backups before putting critical data here.</p></section></div>
      </>}
      {page==='buckets' && <><div className="heading"><div><div className="eyebrow">OBJECT EXPLORER</div><h1>Buckets & objects</h1><p>Organize, preview and manage your files.</p></div><button className="primary" onClick={createBucket}><Plus size={17}/> New bucket</button></div>
        <div className="explorer"><div className="bucket-panel"><div className="panel-subtitle">YOUR BUCKETS <span>{buckets.length}</span></div>{buckets.map(b=><button key={b.name} className={b.name===selected?'bucket-tab selected':'bucket-tab'} onClick={()=>openBucket(b.name)}><Database size={16}/><span>{b.name}</span><ChevronRight size={15}/></button>)}{buckets.length===0&&<p className="muted small">No buckets. Create one to begin.</p>}</div>
        <section className="objects-panel">{!selected?<div className="empty tall"><Database size={34}/><h2>Select a bucket</h2><p>Choose a bucket on the left or create a new one.</p></div>:<>
          <div className="object-header"><div className="path"><button onClick={()=>setPrefix('')}>{selected}</button>{prefix.split('/').filter(Boolean).map((part,i,all)=><React.Fragment key={i}><ChevronRight size={14}/><button onClick={()=>setPrefix(all.slice(0,i+1).join('/')+'/')}>{part}</button></React.Fragment>)}</div><div className="actions"><input ref={fileInput} type="file" multiple hidden onChange={e=>upload(e.target.files)}/><button className="secondary compact" onClick={deleteBucket}><Trash2 size={15}/> Delete bucket</button><button className="primary compact" onClick={()=>fileInput.current?.click()} disabled={busy}><UploadCloud size={16}/>{busy?'Working…':'Upload'}</button></div></div>
          <div className="toolbar"><div className="searchbox"><Search size={17}/><input placeholder="Filter items in this folder..." value={search} onChange={e=>setSearch(e.target.value)}/></div><span className="muted small">{list.items.length} objects · {list.folders.length} folders</span></div>
          <div className="dropzone" onDragOver={e=>e.preventDefault()} onDrop={e=>{e.preventDefault();upload(e.dataTransfer.files)}}><UploadCloud size={18}/> Drop files here to upload into <strong>{prefix||'root'}</strong></div>
          {prefix&&<button className="back-link" onClick={()=>setPrefix(prefix.split('/').filter(Boolean).slice(0,-1).join('/').replace(/(.+)$/, '$1/') )}><ArrowLeft size={16}/> Parent folder</button>}
          <div className="object-table"><div className="table-heading"><span>Name</span><span>Size</span><span>Last modified</span><span>Actions</span></div>
          {list.folders.map(folder=><button key={folder} className="folder-row" onClick={()=>setPrefix(folder)}><span className="filename"><span className="file-symbol"><Folder size={18}/></span>{shortName(folder)}</span><span>—</span><span>Folder</span><span><ChevronRight size={16}/></span></button>)}
          {filtered.map(obj=><div className="file-row" key={obj.key}><div className="filename">{isImage(obj.key)?<img className="thumb" loading="lazy" src={`/api/objects/thumbnail?bucket=${enc(selected)}&key=${enc(obj.key)}&width=96`} onError={e=>{e.currentTarget.style.display='none'}}/>:<span className="file-symbol"><FileIcon size={18}/></span>}<div className="name-stack"><strong title={obj.key}>{shortName(obj.key)}</strong><small>{isImage(obj.key)?'Image':'File'} · S3 object</small></div></div><span className="muted small">{bytes(obj.size)}</span><span className="muted small">{when(obj.modified)}</span><div className="file-actions">{isImage(obj.key)&&<button title="Preview" onClick={()=>setPreview(obj)}><FileImage size={16}/></button>}<a title="Download" href={`/api/objects/download?bucket=${enc(selected)}&key=${enc(obj.key)}`}><ArrowDownToLine size={16}/></a><button title="Copy signed URL" onClick={()=>linkFile(obj.key)}><Link size={16}/></button><button title="Delete" className="danger-icon" onClick={()=>deleteFile(obj.key)}><Trash2 size={16}/></button></div></div>)}
          {filtered.length===0&&list.folders.length===0&&<div className="empty"><Folder size={28}/><strong>Nothing here yet</strong><p>Upload an image or file to get started.</p></div>}
          </div>{list.continuation&&<div className="load-more"><button className="secondary" onClick={()=>reloadObjects(selected,prefix,list.continuation!)}>Load more objects</button></div>}
        </>}</section></div>
      </>}
      {page==='activity'&&<><div className="heading"><div><div className="eyebrow">AUDIT TRAIL</div><h1>Recent activity</h1><p>Administrative actions recorded locally in SQLite.</p></div><button className="secondary" onClick={reloadAudit}><RefreshCw size={17}/> Reload</button></div><section className="panel"><div className="activity-list">{audit.map(e=><div className="activity-row" key={e.id}><div className="row-icon"><Activity size={18}/></div><div><strong>{e.action.replaceAll('_',' ')}</strong><small>{e.bucket||'console'}{e.object_key?` / ${e.object_key}`:''}</small></div><div className="activity-when">{when(e.ts)}</div></div>)}{!audit.length&&<div className="empty"><Activity size={25}/><p>No activity recorded yet.</p></div>}</div></section></>}
      {page==='settings'&&<><div className="heading"><div><div className="eyebrow">CONFIGURATION</div><h1>Settings & deployment</h1><p>Instance configuration and operational guidelines.</p></div></div><section className="panel setting-panel"><h2>Current installation</h2><div className="health-item"><span>Storage engine</span><strong>SeaweedFS (S3 gateway)</strong></div><div className="health-item"><span>Management backend</span><strong>FastAPI</strong></div><div className="health-item"><span>Storage mode</span><strong>On-premises</strong></div><div className="health-item"><span>Health</span><strong className={ready==='online'?'good':'bad'}>{ready==='online'?'Connected':'Unavailable'}</strong></div><p className="panel-note">S3 credentials, admin credentials, disk and PVC size are managed through Docker/Kubernetes environment secrets. This UI intentionally never reveals secrets.</p></section><section className="panel setting-panel"><h2>Production checklist</h2>{['Configure TLS and secure cookies','Rotate generated S3/admin credentials','Use persistent Kubernetes volumes','Configure tested off-host backups','Enable network policies and rate limiting','Run S3 SDK compatibility and restore tests'].map(x=><div className="check-row" key={x}><CheckCircle2 size={18}/>{x}</div>)}</section></>}
      </div>
    </main>
    {preview&&selected&&<div className="modal-backdrop" onClick={()=>setPreview(null)}><div className="preview-modal" onClick={e=>e.stopPropagation()}><div className="modal-header"><div><strong>{shortName(preview.key)}</strong><small>{bytes(preview.size)} · {when(preview.modified)}</small></div><button className="icon-btn" onClick={()=>setPreview(null)}><X/></button></div><img src={`/api/objects/preview?bucket=${enc(selected)}&key=${enc(preview.key)}`} alt={shortName(preview.key)}/><div className="modal-actions"><button className="secondary" onClick={()=>linkFile(preview.key)}><Copy size={16}/> Copy signed link</button><a className="primary" href={`/api/objects/download?bucket=${enc(selected)}&key=${enc(preview.key)}`}><ArrowDownToLine size={17}/> Download original</a></div></div></div>}
    {notice&&<div className="toast"><span>{notice}</span><button onClick={()=>setNotice('')}><X size={14}/></button></div>}
  </div>;
}

createRoot(document.getElementById('root')!).render(<App/>);
