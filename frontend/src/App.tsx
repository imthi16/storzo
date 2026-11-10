import React, { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { Activity, ArrowDown, ArrowDownToLine, ArrowUp, ChevronRight, Copy, Database, LayoutDashboard, LogOut, Menu, RefreshCw, Settings, X } from 'lucide-react';
import type { Session, Bucket, Obj, Listing, Stats, SortKey, Preview, Audit, Page, ActivityPage, UploadItem, DialogState } from './types';
import { plural, enc, bytes, isImage, shortName, folderOf, when, bucketNameError } from './lib/format';
import { pageFromHash, hashFor } from './lib/routes';
import { ApiError, call } from './api/client';
import { postUpload } from './api/upload';
import { PAGE_SIZES } from './constants';
import { AppDialog } from './components/AppDialog';
import { LoginPage } from './pages/LoginPage';
import { OverviewPage } from './pages/OverviewPage';
import { BucketsPage } from './pages/BucketsPage';
import { ActivityListPage } from './pages/ActivityListPage';
import { ActivityEventPage } from './pages/ActivityEventPage';
import { SettingsPage } from './pages/SettingsPage';

export function App() {
  const [session, setSession] = useState<Session | null>(null);
  const [booting, setBooting] = useState(true);
  const [page, setPage] = useState<Page>(() => pageFromHash().page);
  const [activityId, setActivityId] = useState<number | null>(() => pageFromHash().id);
  const [menuOpen, setMenuOpen] = useState(false);
  const [username, setUsername] = useState('admin');
  const [password, setPassword] = useState('');
  const [loginBusy, setLoginBusy] = useState(false);
  const [notice, setNotice] = useState('');
  const [buckets, setBuckets] = useState<Bucket[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [prefix, setPrefix] = useState('');
  const [pageSize, setPageSize] = useState(() => { try { const v = Number(localStorage.getItem('storzo.pageSize')); return PAGE_SIZES.includes(v) ? v : 10; } catch { return 10; } });
  const [barPage, setBarPage] = useState(0);
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
    setPager({index: 0, tokens: ['']});
  }, [selected, prefix, session, pageSize]);
  const [stats, setStats] = useState<Stats | null>(null);
  const [statsAt, setStatsAt] = useState<number | null>(null);
  const [audit, setAudit] = useState<Audit[]>([]);
  const [activity, setActivity] = useState<ActivityPage | null>(null);
  const [activityOffset, setActivityOffset] = useState(0);
  const [entry, setEntry] = useState<Audit | null>(null);
  const [entryError, setEntryError] = useState('');
  const [pager, setPager] = useState<{index: number; tokens: string[]}>({index: 0, tokens: ['']});
  const [bucketFilter, setBucketFilter] = useState('');
  const [dragging, setDragging] = useState(false);
  const dragDepth = useRef(0);
  const [ready, setReady] = useState<'checking'|'online'|'offline'>('checking');
  const [busy, setBusy] = useState(false);
  const [search, setSearch] = useState('');
  const [dialog, setDialog] = useState<DialogState | null>(null);
  const closeDialog = (v: string | boolean | null) => { dialog?.resolve(v); setDialog(null); };
  const askText = (title: string, label: string, confirmLabel: string, validate?: (v: string) => string, submit?: (v: string) => Promise<string>) => new Promise<string | null>(resolve => setDialog({submit, validate, title, label, confirmLabel, text: true, resolve: v => resolve(v as string | null)}));
  const askConfirm = (title: string, message: string, confirmLabel: string) => new Promise<boolean>(resolve => setDialog({title, message, confirmLabel, text: false, danger: true, resolve: v => resolve(v === true)}));
  const [preview, setPreview] = useState<Preview | null>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const [uploads, setUploads] = useState<UploadItem[]>([]);
  const uploadSeq = useRef(0);
  const [sortKey, setSortKey] = useState<SortKey>('name');
  const [sortDir, setSortDir] = useState<1 | -1>(1);
  const [typeFilter, setTypeFilter] = useState<'all' | 'images' | 'other'>('all');
  const [view, setView] = useState<'list' | 'grid'>(() => {
    try { return window.localStorage.getItem('storzo-objects-view') === 'grid' ? 'grid' : 'list'; }
    catch { return 'list'; }
  });
  const [checked, setChecked] = useState<string[]>([]);
  const [now, setNow] = useState(() => Date.now());
  const lastRefresh = useRef(0);

  const csrf = session?.csrf;
  const show = (m: string) => { setNotice(m); window.setTimeout(() => setNotice(''), 6000); };
  const fail = (e: unknown) => {
    if (e instanceof ApiError && e.isSession) { setSession(null); show(e.message); return; }
    show(e instanceof Error ? e.message : 'Something went wrong. Try again.');
  };

  // Preview modal closes on Escape.
  useEffect(() => {
    if (!preview) return;
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') setPreview(null); };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [preview]);
  const reloadBuckets = async () => { const d = await call<{items: Bucket[]}>('/buckets'); setBuckets(d.items); };
  const reloadStats = async () => { const d = await call<Stats>('/dashboard'); setStats(d); setStatsAt(Date.now()); };
  const reloadAudit = async () => { const d = await call<ActivityPage>('/audit?limit=200'); setAudit(d.items); };
  const reloadActivity = async (offset = activityOffset) => { const d = await call<ActivityPage>(`/audit?limit=${pageSize}&offset=${offset}`); setActivity(d); };
  const reloadObjects = async (bucket = selected, path = prefix, token = pager.tokens[pager.index] ?? '') => {
    if (!bucket || bucket !== objectLocation.current.bucket || path !== objectLocation.current.prefix) return;
    const request = ++listingRequest.current;
    const p = `/buckets/${enc(bucket)}/objects?prefix=${enc(path)}&limit=${pageSize}${token ? `&continuation=${enc(token)}` : ''}`;
    try {
      const data = await call<Listing>(p);
      if (request !== listingRequest.current) return;
      setListing({bucket, data});
      // The last item of a later page was removed: step back so the user is not left on an empty page.
      if (data.items.length === 0 && data.folders.length === 0 && pager.index > 0 && token) {
        const back = pager.tokens[pager.index - 1];
        setPager(old => ({index: old.index - 1, tokens: old.tokens.slice(0, old.index)}));
        await reloadObjects(bucket, path, back);
      }
    } catch (error) {
      if (request === listingRequest.current) throw error;
    }
  };
  const goNextObjects = () => {
    const next = list.continuation; if (!next || !selected) return;
    setPager(old => ({index: old.index + 1, tokens: [...old.tokens.slice(0, old.index + 1), next]}));
    setChecked([]); reloadObjects(selected, prefix, next).catch(fail);
  };
  const goPrevObjects = () => {
    if (pager.index === 0 || !selected) return;
    const token = pager.tokens[pager.index - 1];
    setPager(old => ({index: old.index - 1, tokens: old.tokens}));
    setChecked([]); reloadObjects(selected, prefix, token).catch(fail);
  };
  const changePageSize = (n: number) => { setPageSize(n); setActivityOffset(0); setBarPage(0); try { localStorage.setItem('storzo.pageSize', String(n)); } catch { /* optional */ } };
  const refresh = async () => {
    setBusy(true);
    try {
      if (page === 'dashboard') await Promise.all([reloadStats(), reloadBuckets(), reloadAudit()]);
      if (page === 'buckets') { await reloadBuckets(); if (selected) await reloadObjects(); }
      if (page === 'activity') await reloadActivity();
      const res = await fetch('/api/health/ready'); setReady(res.ok ? 'online' : 'offline');
    } catch(e) { fail(e); } finally { setBusy(false); }
  };

  useEffect(() => { call<Session>('/auth/me').then(setSession).catch(() => setSession(null)).finally(() => setBooting(false)); }, []);
  useEffect(() => { const h = hashFor(page, activityId); if (location.hash !== h) location.hash = h; }, [page, activityId]);
  useEffect(() => { const on = () => { const r = pageFromHash(); setPage(r.page); setActivityId(r.id); }; window.addEventListener('hashchange', on); return () => window.removeEventListener('hashchange', on); }, []);
  useEffect(() => { if (!session || page !== 'activity') return; reloadActivity().catch(fail); }, [session, page, activityOffset, pageSize]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!session || page !== 'activity-detail' || !activityId) return;
    let live = true; setEntry(null); setEntryError('');
    call<Audit>(`/audit/${activityId}`).then(e => { if (live) setEntry(e); })
      .catch(e => { if (live) { if (e instanceof ApiError && e.isSession) fail(e); else setEntryError(e instanceof ApiError && e.status === 404 ? 'This activity entry no longer exists. Go back to the list to see current events.' : e instanceof Error ? e.message : 'Could not load this event.'); } });
    return () => { live = false; };
  }, [session, page, activityId]); // eslint-disable-line react-hooks/exhaustive-deps
  // Dropping a file outside the drop area must not make the browser navigate to it.
  useEffect(() => {
    const stop = (e: DragEvent) => { if (e.dataTransfer?.types?.includes('Files')) e.preventDefault(); };
    window.addEventListener('dragover', stop); window.addEventListener('drop', stop);
    return () => { window.removeEventListener('dragover', stop); window.removeEventListener('drop', stop); };
  }, []);
  useEffect(() => { if (!session) return; refresh(); }, [session, page]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { if (!session || page !== 'buckets' || !selected) return; reloadObjects(selected, prefix, '').catch(fail); }, [selected, prefix, pageSize]); // eslint-disable-line react-hooks/exhaustive-deps
  // Dashboard auto-refresh every 30s, only while the tab is visible; timer cleared on unmount/page change.
  useEffect(() => {
    if (!session || page !== 'dashboard') return;
    lastRefresh.current = Date.now();
    const tick = async () => {
      setNow(Date.now());
      if (document.visibilityState !== 'visible' || Date.now() - lastRefresh.current < 30000) return;
      lastRefresh.current = Date.now();
      try {
        await Promise.all([reloadStats(), reloadBuckets(), reloadAudit()]);
        const res = await fetch('/api/health/ready'); setReady(res.ok ? 'online' : 'offline');
      } catch(e) { fail(e); }
    };
    const t = window.setInterval(() => { void tick(); }, 5000);
    return () => window.clearInterval(t);
  }, [session, page]); // eslint-disable-line react-hooks/exhaustive-deps
  // Selection never survives a bucket/folder change.
  useEffect(() => { setChecked([]); }, [selected, prefix]);

  const login = async (event: React.FormEvent) => {
    event.preventDefault(); setLoginBusy(true);
    try { const data = await call<Session>('/auth/login', undefined, {method:'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({username, password})}); setSession(data); setPassword(''); }
    catch(e) { fail(e); } finally { setLoginBusy(false); }
  };
  const logout = async () => { try { await call('/auth/logout', csrf, {method:'POST'}); } catch(e) { fail(e); } setSession(null); setSelected(null); };
  const createBucket = async () => {
    const name = await askText('New bucket', 'Name (3-63 lowercase letters, numbers, hyphens)', 'Create bucket', bucketNameError, async value => {
      try { await call('/buckets', csrf, {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({name: value})}); return ''; }
      catch (e) { if (e instanceof ApiError && e.isSession) { fail(e); throw e; } return e instanceof Error ? e.message : 'Could not create the bucket. Try again.'; }
    });
    if (!name) return;
    setBusy(true);
    try { await reloadBuckets(); setSelected(name); setPrefix(''); setPage('buckets'); show('Bucket created'); }
    catch(e) { fail(e); } finally { setBusy(false); }
  };
  const deleteBucket = async () => {
    if (!selected || !(await askConfirm('Delete bucket', `Delete "${selected}"? Only empty buckets can be deleted. This cannot be undone.`, 'Delete'))) return;
    try { await call(`/buckets/${enc(selected)}`, csrf, {method:'DELETE'}); setSelected(null); setPrefix(''); await reloadBuckets(); show('Bucket removed'); } catch(e) { fail(e); }
  };
  // runUpload uses only the bucket/key stored on the item when it was queued,
  // never the live selected/prefix state, so a retry after navigating elsewhere
  // still uploads to the original location. Returns true on success.
  const runUpload = async (item: UploadItem): Promise<boolean> => {
    if (!csrf) return false;
    setUploads(old => old.map(u => u.id === item.id ? {...u, status: 'uploading', progress: 0, error: ''} : u));
    try {
      await postUpload(item.bucket, item.key, item.file, csrf, pct =>
        setUploads(old => old.map(u => u.id === item.id ? {...u, progress: pct} : u)));
      setUploads(old => old.map(u => u.id === item.id ? {...u, status: 'done', progress: 100} : u));
      return true;
    } catch (e) {
      const err = e instanceof ApiError ? e : null;
      const message = `${item.name}: ${err ? err.message : 'The upload failed. Try again.'}`;
      setUploads(old => old.map(u => u.id === item.id ? {...u, status: 'error', error: message, retry: err ? err.retryable : true} : u));
      if (err?.isSession) setSession(null);
      show(message);
      return false;
    }
  };
  // Refresh the object listing for a retried item, but only when its bucket is
  // the one currently on screen; otherwise leave the current view untouched.
  const refreshAfterRetry = async (item: UploadItem) => {
    if (item.bucket !== objectLocation.current.bucket) return;
    try { await reloadObjects(item.bucket, folderOf(item.key)); await reloadStats(); } catch(e) { fail(e); }
  };
  const upload = async (files: FileList | File[] | null) => {
    if (!selected || !files?.length) return;
    const bucket = selected, folder = prefix;
    const batch: UploadItem[] = Array.from(files).map(file =>
      ({id: ++uploadSeq.current, name: file.name, size: file.size, status: 'queued' as const, progress: 0, error: '', file, bucket, key: folder + file.name, retry: true}));
    setUploads(old => [...batch, ...old].slice(0, 20));
    if (fileInput.current) fileInput.current.value = '';
    setBusy(true);
    for (const item of batch) await runUpload(item);
    setBusy(false);
    try { await reloadObjects(); await reloadStats(); } catch(e) { fail(e); }
  };
  const retryUpload = async (id: number) => {
    const item = uploads.find(u => u.id === id);
    if (!item || item.status !== 'error') return;
    setBusy(true);
    const ok = await runUpload(item);
    setBusy(false);
    if (ok) await refreshAfterRetry(item);
  };
  const retryFailed = async () => {
    const failed = uploads.filter(u => u.status === 'error' && u.retry);
    setBusy(true);
    const succeeded: UploadItem[] = [];
    for (const item of failed) { if (await runUpload(item)) succeeded.push(item); }
    setBusy(false);
    for (const item of succeeded) await refreshAfterRetry(item);
  };
  const failedCount = uploads.filter(u => u.status === 'error' && u.retry).length;
  const deleteFile = async (key: string) => {
    if (!selected || !(await askConfirm('Delete object', `Permanently delete "${shortName(key)}"? This cannot be undone.`, 'Delete'))) return;
    try { await call(`/objects?bucket=${enc(selected)}&key=${enc(key)}`, csrf, {method:'DELETE'}); await reloadObjects(); show('Object deleted'); } catch(e) { fail(e); }
  };
  const linkFile = async (key: string, bucket: string | null = selected) => {
    if (!bucket) return;
    try { const res = await call<{url:string}>(`/objects/presign?bucket=${enc(bucket)}&key=${enc(key)}&expires=300`); await navigator.clipboard.writeText(res.url); show('5-minute signed URL copied'); }
    catch(e) { fail(e); }
  };
  const nav = (p: Page) => { setPage(p); if (p !== 'activity-detail') setActivityId(null); setMenuOpen(false); };
  const openActivity = (id: number) => { setActivityId(id); setPage('activity-detail'); };
  const openLocation = (bucket: string, key: string | null) => { setSelected(bucket); setPrefix(key ? folderOf(key) : ''); setSearch(''); setPage('buckets'); setActivityId(null); };
  const onDragEnter = (e: React.DragEvent) => { if (!e.dataTransfer.types.includes('Files')) return; e.preventDefault(); dragDepth.current++; setDragging(true); };
  const onDragOverPanel = (e: React.DragEvent) => { if (!e.dataTransfer.types.includes('Files')) return; e.preventDefault(); e.dataTransfer.dropEffect = selected ? 'copy' : 'none'; };
  const onDragLeave = () => { dragDepth.current = Math.max(0, dragDepth.current - 1); if (dragDepth.current === 0) setDragging(false); };
  const onDropFiles = (e: React.DragEvent) => {
    if (!e.dataTransfer.types.includes('Files')) return;
    e.preventDefault(); dragDepth.current = 0; setDragging(false);
    if (!selected) { show('Open a bucket first, then drop your files into it.'); return; }
    show(`Uploading ${plural(e.dataTransfer.files.length, 'file')} to ${selected}`);
    void upload(e.dataTransfer.files);
  };
  const clickSort = (k: SortKey) => {
    if (k === sortKey) setSortDir(d => (d === 1 ? -1 : 1));
    else { setSortKey(k); setSortDir(1); }
  };
  const sortArrow = (k: SortKey) => sortKey !== k ? null : (sortDir === 1 ? <ArrowUp size={12}/> : <ArrowDown size={12}/>);
  const sortLabel = (k: SortKey, name: string) => `Sort by ${name}${sortKey === k ? (sortDir === 1 ? ', ascending' : ', descending') : ''}`;
  const setViewPersist = (v: 'list' | 'grid') => {
    setView(v);
    try { window.localStorage.setItem('storzo-objects-view', v); } catch { /* private mode */ }
  };
  const toggleCheck = (key: string) => setChecked(old => old.includes(key) ? old.filter(k => k !== key) : [...old, key]);
  const filtered = list.items
    .filter(o => shortName(o.key).toLowerCase().includes(search.toLowerCase()))
    .filter(o => typeFilter === 'all' ? true : typeFilter === 'images' ? isImage(o.key) : !isImage(o.key))
    .sort((a, b) => sortDir * (sortKey === 'size' ? a.size - b.size
      : sortKey === 'modified' ? a.modified.localeCompare(b.modified)
      : shortName(a.key).localeCompare(shortName(b.key))));
  const checkedBytes = list.items.filter(o => checked.includes(o.key)).reduce((a, o) => a + o.size, 0);
  const deleteSelected = async () => {
    if (!selected || checked.length === 0) return;
    if (!(await askConfirm('Delete selected', `Permanently delete ${plural(checked.length, 'object')} from "${selected}"? This cannot be undone.`, 'Delete'))) return;
    setBusy(true);
    let ok = 0; const failed: string[] = [];
    for (const key of checked) {
      try { await call(`/objects?bucket=${enc(selected)}&key=${enc(key)}`, csrf, {method: 'DELETE'}); ok++; }
      catch { failed.push(shortName(key)); }
    }
    setChecked([]);
    setBusy(false);
    try { await reloadObjects(); } catch(e) { fail(e); }
    show(failed.length === 0 ? `Deleted ${ok} object(s)` : `Deleted ${ok}, failed ${failed.length}: ${failed.slice(0, 3).join(', ')}${failed.length > 3 ? '…' : ''}`);
  };
  const openBucket = (name: string) => { setSelected(name); setPrefix(''); setSearch(''); setPage('buckets'); };
  const openPreview = (obj: Obj, bucket: string) => setPreview({obj, bucket});
  // Upload counts per day for the last 14 days, from audit entries whose action contains "upload".
  const uploadDays = (() => {
    const out = Array.from({length: 14}, (_, i) => {
      const d = new Date(); d.setDate(d.getDate() - (13 - i));
      return {day: d.toISOString().slice(0, 10), label: d.toLocaleDateString(undefined, {month: 'short', day: 'numeric'}), count: 0};
    });
    for (const e of audit) {
      if (!e.action.toLowerCase().includes('upload')) continue;
      const day = (e.ts || '').slice(0, 10);
      const slot = out.find(s => s.day === day);
      if (slot) slot.count++;
    }
    return out;
  })();
  const updatedAgo = statsAt == null ? null : Math.max(0, Math.round((now - statsAt) / 1000));

  if (booting) return <div className="loading"><Database size={35}/><p>Connecting to Storzo…</p></div>;
  if (!session) return <LoginPage login={login} username={username} setUsername={setUsername} password={password} setPassword={setPassword} loginBusy={loginBusy} notice={notice}/>;

  return <div className="shell">
    <aside className={`sidebar ${menuOpen?'expanded':''}`}>
      <div className="brand"><div className="brand-icon"><Database size={22}/></div><div><strong>Storzo</strong><small>STORAGE CONSOLE</small></div></div>
      
      <nav>
        <button className={page==='dashboard'?'nav active':'nav'} onClick={()=>nav('dashboard')}><LayoutDashboard size={19}/>Overview</button>
        <button className={page==='buckets'?'nav active':'nav'} onClick={()=>nav('buckets')}><Database size={19}/>Buckets & objects</button>
        <button className={page==='activity'||page==='activity-detail'?'nav active':'nav'} onClick={()=>nav('activity')}><Activity size={19}/>Activity log</button>
        <button className={page==='settings'?'nav active':'nav'} onClick={()=>nav('settings')}><Settings size={19}/>Settings</button>
      </nav>
      <div className="side-bottom"><div className="infra-tag"><span className={`pulse ${ready}`}/>{ready==='online'?'Storage connected':ready==='offline'?'Storage unavailable':'Checking storage'}</div><div className="side-user"><div className="avatar">{session.username[0].toUpperCase()}</div><div><strong>{session.username}</strong><small>Administrator</small></div><button onClick={logout} title="Sign out" aria-label="Sign out" className="icon-btn"><LogOut size={17}/></button></div></div>
    </aside>
    <main className="content">
      <header className="topbar"><button className="mobile-toggle icon-btn" aria-label="Toggle navigation menu" onClick={()=>setMenuOpen(!menuOpen)}><Menu/></button><nav className="breadcrumbs" aria-label="Breadcrumb"><button className="crumb crumb-root" onClick={()=>nav('dashboard')}>Workspace</button><ChevronRight size={15} className="crumb-root"/>{page==='activity-detail'?<><button className="crumb" onClick={()=>nav('activity')}>Activity log</button><ChevronRight size={15}/><strong aria-current="page">Event #{activityId}</strong></>:<strong aria-current="page">{page==='dashboard'?'Overview':page==='buckets'?'Buckets & objects':page==='activity'?'Activity log':'Settings'}</strong>}</nav><div className="top-actions"><button className="secondary compact" onClick={refresh} disabled={busy}><RefreshCw size={16} className={busy?'spin':''}/> Refresh</button></div></header>
      <div className="page-body">
      {page==='dashboard' && <OverviewPage stats={stats} updatedAgo={updatedAgo} uploadDays={uploadDays} ready={ready} barPage={barPage} setBarPage={setBarPage} pageSize={pageSize} changePageSize={changePageSize} openBucket={openBucket} openPreview={openPreview} nav={nav}/>}
      {page==='buckets' && <BucketsPage createBucket={createBucket} buckets={buckets} bucketFilter={bucketFilter} setBucketFilter={setBucketFilter} selected={selected} openBucket={openBucket} onDragEnter={onDragEnter} onDragOverPanel={onDragOverPanel} onDragLeave={onDragLeave} onDropFiles={onDropFiles} dragging={dragging} prefix={prefix} setPrefix={setPrefix} fileInput={fileInput} upload={upload} deleteBucket={deleteBucket} busy={busy} search={search} setSearch={setSearch} typeFilter={typeFilter} setTypeFilter={setTypeFilter} view={view} setViewPersist={setViewPersist} checked={checked} setChecked={setChecked} checkedBytes={checkedBytes} deleteSelected={deleteSelected} toggleCheck={toggleCheck} uploads={uploads} setUploads={setUploads} retryUpload={retryUpload} failedCount={failedCount} retryFailed={retryFailed} listing={listing} list={list} filtered={filtered} openPreview={openPreview} linkFile={linkFile} deleteFile={deleteFile} sortLabel={sortLabel} clickSort={clickSort} sortArrow={sortArrow} pageSize={pageSize} changePageSize={changePageSize} pager={pager} goPrevObjects={goPrevObjects} goNextObjects={goNextObjects}/>}
      {page==='activity' && <ActivityListPage activity={activity} pageSize={pageSize} changePageSize={changePageSize} setActivityOffset={setActivityOffset} openActivity={openActivity} reloadActivity={reloadActivity} fail={fail}/>}
      {page==='activity-detail' && <ActivityEventPage entry={entry} entryError={entryError} activityId={activityId} nav={nav} openLocation={openLocation}/>}
      {page==='settings' && <SettingsPage ready={ready}/>}
      </div>
    </main>
    {preview&&<div className="modal-backdrop" onClick={()=>setPreview(null)}><div className="preview-modal" role="dialog" aria-modal="true" aria-label={`Preview of ${shortName(preview.obj.key)}`} data-testid="preview-modal" onClick={e=>e.stopPropagation()}><div className="modal-header"><div><strong>{shortName(preview.obj.key)}</strong><small>{preview.bucket} · {bytes(preview.obj.size)} · {when(preview.obj.modified)}</small></div><button className="icon-btn" aria-label="Close preview" autoFocus onClick={()=>setPreview(null)}><X/></button></div><img src={`/api/objects/preview?bucket=${enc(preview.bucket)}&key=${enc(preview.obj.key)}`} alt={shortName(preview.obj.key)}/><div className="modal-actions"><button className="secondary" onClick={()=>linkFile(preview.obj.key, preview.bucket)}><Copy size={16}/> Copy signed link</button><a className="primary" href={`/api/objects/download?bucket=${enc(preview.bucket)}&key=${enc(preview.obj.key)}`}><ArrowDownToLine size={17}/> Download original</a></div></div></div>}
    {dialog&&<AppDialog d={dialog} onClose={closeDialog}/>}
    {notice&&<div className="toast" role="status"><span>{notice}</span><button aria-label="Dismiss notification" onClick={()=>setNotice('')}><X size={14}/></button></div>}
  </div>;
}
