import React, { useEffect, useState } from 'react';
import type { DialogState } from '../types';

export function AppDialog({d, onClose}: {d: DialogState; onClose: (v: string | boolean | null) => void}) {
  const [value, setValue] = useState('');
  const [serverError, setServerError] = useState('');
  const [working, setWorking] = useState(false);
  const cancel = () => onClose(d.text ? null : false);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') cancel(); };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  });
  const problem = (d.text && d.validate ? d.validate(value.trim()) : '') || serverError;
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!d.text) { onClose(true); return; }
    if (!value.trim() || problem || working) return;
    if (!d.submit) { onClose(value.trim()); return; }
    setWorking(true);
    try {
      const err = await d.submit(value.trim());
      if (err) setServerError(err); else onClose(value.trim());
    } catch { onClose(null); } finally { setWorking(false); }
  };
  return <div className="modal-backdrop" onClick={cancel}>
    <form className="dialog" role="dialog" aria-modal="true" aria-labelledby="dialog-title" data-testid="app-dialog" onClick={e => e.stopPropagation()} onSubmit={submit}>
      <h2 id="dialog-title">{d.title}</h2>
      {d.message && <p>{d.message}</p>}
      {d.text && <label>{d.label}<input autoFocus value={value} onChange={e => { setValue(e.target.value); setServerError(''); }} autoComplete="off" spellCheck={false} aria-invalid={!!problem} aria-describedby={problem ? 'dialog-error' : undefined} /></label>}
      {problem && <p id="dialog-error" className="dialog-error" role="alert">{problem}</p>}
      <div className="dialog-actions">
        <button type="button" className="secondary" onClick={cancel}>Cancel</button>
        <button type="submit" className={d.danger ? 'danger' : 'primary'} autoFocus={!d.text} disabled={d.text && (!value.trim() || !!problem || working)}>{working ? 'Working…' : d.confirmLabel}</button>
      </div>
    </form>
  </div>;
}
