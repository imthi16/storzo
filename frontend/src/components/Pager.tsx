import { ChevronLeft, ChevronRight } from 'lucide-react';
import { PAGE_SIZES } from '../constants';

export function Pager({pageNumber, pageSize, onSize, hasPrev, hasNext, onPrev, onNext, summary, disabled}: {pageNumber: number; pageSize: number; onSize: (n: number) => void; hasPrev: boolean; hasNext: boolean; onPrev: () => void; onNext: () => void; summary: string; disabled?: boolean}) {
  return <nav className="pager" aria-label="Pagination">
    <label className="pager-size">Rows per page
      <select value={pageSize} onChange={e => onSize(Number(e.target.value))} aria-label="Rows per page">{PAGE_SIZES.map(n => <option key={n} value={n}>{n}</option>)}</select>
    </label>
    <span className="pager-summary" aria-live="polite">{summary}</span>
    <div className="pager-nav">
      <button className="secondary compact" onClick={onPrev} disabled={!hasPrev || disabled} data-testid="pager-prev"><ChevronLeft size={15}/> Previous</button>
      <span className="pager-page">Page {pageNumber}</span>
      <button className="secondary compact" onClick={onNext} disabled={!hasNext || disabled} data-testid="pager-next">Next <ChevronRight size={15}/></button>
    </div>
  </nav>;
}
