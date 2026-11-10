import { Activity, ChevronRight, RefreshCw } from 'lucide-react';
import type { ActivityPage } from '../types';
import { when } from '../lib/format';
import { Pager } from '../components/Pager';

type Props = {
  activity: ActivityPage | null; pageSize: number; changePageSize: (n: number) => void; setActivityOffset: (n: number) => void;
  openActivity: (id: number) => void; reloadActivity: () => Promise<void>; fail: (e: unknown) => void;
};

export function ActivityListPage({activity, pageSize, changePageSize, setActivityOffset, openActivity, reloadActivity, fail}: Props) {
  return <>
      <div className="heading"><div><h1>Recent activity</h1><p>Administrative actions recorded locally in SQLite. Select an event to see its details.</p></div><button className="secondary" onClick={()=>reloadActivity().catch(fail)}><RefreshCw size={17}/> Reload</button></div><section className="panel activity-panel"><div className="activity-list">{!activity?[0,1,2,3].map(i=><div className="activity-row" key={i}><div className="skel skel-thumb"/><div className="skel skel-line"/></div>):activity.items.map(e=><button className="activity-row" key={e.id} onClick={()=>openActivity(e.id)} aria-label={`Open event ${e.id}: ${e.action.replaceAll('_',' ')}`}><div className="row-icon"><Activity size={18}/></div><div><strong>{e.action.replaceAll('_',' ')}</strong><small>{e.bucket||'console'}{e.object_key?` / ${e.object_key}`:''}</small></div><div className="activity-when">{when(e.ts)}</div><ChevronRight size={16}/></button>)}{activity&&!activity.items.length&&<div className="empty"><Activity size={25}/><p>No activity recorded yet.</p></div>}</div>{activity&&<Pager pageNumber={Math.floor(activity.offset/pageSize)+1} pageSize={pageSize} onSize={changePageSize} hasPrev={activity.offset>0} hasNext={activity.offset+activity.items.length<activity.total} onPrev={()=>setActivityOffset(Math.max(0,activity.offset-pageSize))} onNext={()=>setActivityOffset(activity.offset+pageSize)} summary={activity.total?`Showing ${activity.offset+1}-${activity.offset+activity.items.length} of ${activity.total.toLocaleString()}`:'No events'} />}</section>
  </>;
}
