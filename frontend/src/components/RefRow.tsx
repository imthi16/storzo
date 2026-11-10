import { ArrowDownToLine, File as FileIcon, FileImage } from 'lucide-react';
import type { RefItem } from '../types';
import { enc, bytes, isImage, shortName, whenShort } from '../lib/format';

/** One row for both "Recent uploads" and "Largest objects": thumbnail, name, bucket + time, size, actions. */
export function RefRow({r, onOpenBucket, onPreview}: {r: RefItem; onOpenBucket: (b: string) => void; onPreview: (r: RefItem) => void}) {
  const name = shortName(r.key);
  return <li className="ref-row">
    {isImage(r.key)
      ? <button className="ref-thumb" onClick={() => onPreview(r)} aria-label={`Preview ${name}`}><img loading="lazy" alt="" src={`/api/objects/thumbnail?bucket=${enc(r.bucket)}&key=${enc(r.key)}&width=96`} onError={e => { e.currentTarget.style.display = 'none'; }} /></button>
      : <span className="ref-thumb ref-icon" aria-hidden="true"><FileIcon size={16}/></span>}
    <div className="ref-meta"><strong title={r.key}>{name}</strong><small><button className="link-btn" onClick={() => onOpenBucket(r.bucket)}>{r.bucket}</button> · {whenShort(r.modified)}</small></div>
    <span className="rank">{bytes(r.size)}</span>
    <span className="ref-actions">
      {isImage(r.key) ? <button className="icon-btn" title="Preview" aria-label={`Preview ${name}`} onClick={() => onPreview(r)}><FileImage size={16}/></button> : <span className="icon-btn-gap" aria-hidden="true"/>}
      <a className="icon-btn" title="Download" aria-label={`Download ${name}`} href={`/api/objects/download?bucket=${enc(r.bucket)}&key=${enc(r.key)}`}><ArrowDownToLine size={16}/></a>
    </span>
  </li>;
}
