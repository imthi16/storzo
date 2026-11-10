import type { TypeStat } from '../types';
import { plural, bytes } from '../lib/format';
import { TYPE_COLORS } from '../constants';

export function TypeDonut({data}: {data: TypeStat[]}) {
  const total = data.reduce((a, d) => a + d.bytes, 0);
  const summary = data.map(d => `${d.type}: ${plural(d.count, 'file')}, ${bytes(d.bytes)}`).join('; ');
  return <div className="type-mix">
    <div className="type-total"><strong>{bytes(total)}</strong><small>stored, by file type</small></div>
    <div className="type-bar" role="img" aria-label={`Storage by file type. ${summary}`}>
      {data.map(d => <span key={d.type} style={{flexGrow: Math.max(d.bytes, 1), background: TYPE_COLORS[d.type]}} />)}
    </div>
    <ul className="legend">{data.map(d =>
      <li key={d.type}><span className="swatch" style={{background: TYPE_COLORS[d.type]}} aria-hidden="true" /><strong>{d.type}</strong><small>{plural(d.count, 'file')} · {bytes(d.bytes)}</small></li>)}
    </ul>
  </div>;
}
