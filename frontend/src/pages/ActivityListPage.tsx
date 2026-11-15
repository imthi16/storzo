import { Activity, ChevronRight, RefreshCw } from 'lucide-react';
import type { ActivityPage } from '../types';
import { activityRowLabel, activityTitle, parseGatewayDetail, when, type AuditDecision, type AuditSource } from '../lib/format';
import { Pager } from '../components/Pager';

type Props = {
  activity: ActivityPage | null; pageSize: number; changePageSize: (n: number) => void; setActivityOffset: (n: number) => void;
  openActivity: (id: number) => void; reloadActivity: () => Promise<void>; fail: (e: unknown) => void;
  source: AuditSource; decision: AuditDecision;
  setSource: (source: AuditSource) => void; setDecision: (decision: AuditDecision) => void;
};

const SOURCES: {id: AuditSource; label: string}[] = [
  {id: 'all', label: 'All'},
  {id: 'console', label: 'Console'},
  {id: 'gateway', label: 'Gateway'},
];
const DECISIONS: {id: AuditDecision; label: string}[] = [
  {id: 'all', label: 'All'},
  {id: 'allow', label: 'Allow'},
  {id: 'deny', label: 'Deny'},
];

export function ActivityListPage({activity, pageSize, changePageSize, setActivityOffset, openActivity, reloadActivity, fail, source, decision, setSource, setDecision}: Props) {
  return <>
    <div className="heading">
      <div>
        <h1>Recent activity</h1>
        <p>Console actions and agent gateway requests. Select an event to see its details.</p>
      </div>
      <button className="secondary" onClick={() => reloadActivity().catch(fail)}><RefreshCw size={17}/> Reload</button>
    </div>
    <div className="activity-filters" role="group" aria-label="Filter activity">
      <span className="filter-name">Source</span>
      {SOURCES.map(item => <button type="button" key={item.id} aria-pressed={source === item.id} onClick={() => setSource(item.id)}>{item.label}</button>)}
      {source === 'gateway' && <>
        <span className="filter-name">Decision</span>
        {DECISIONS.map(item => <button type="button" key={item.id} aria-pressed={decision === item.id} onClick={() => setDecision(item.id)}>{item.label}</button>)}
      </>}
    </div>
    <section className="panel activity-panel">
      <div className="activity-list">
        {!activity ? [0, 1, 2, 3].map(i => <div className="activity-row" key={i}><div className="skel skel-thumb"/><div className="skel skel-line"/></div>) : activity.items.map(e => {
          const title = activityTitle(e.action, e.detail);
          const gw = e.action === 'gateway.request' ? parseGatewayDetail(e.detail) : null;
          const where = `${e.bucket || 'console'}${e.object_key ? ` / ${e.object_key}` : ''}`;
          return <button className="activity-row" key={e.id} onClick={() => openActivity(e.id)} aria-label={activityRowLabel(e)}>
            <div className="row-icon"><Activity size={18}/></div>
            <div className="activity-main">
              <div className="activity-title">
                <strong>{title}</strong>
                {gw && <>
                  <span className="activity-method">{gw.method}</span>
                  <span className={`activity-chip is-${gw.decision}`}>{gw.decision}</span>
                  <span className="activity-code">{gw.status}</span>
                </>}
              </div>
              <small>{where}</small>
            </div>
            <div className="activity-when">{when(e.ts)}</div>
            <ChevronRight size={16}/>
          </button>;
        })}
        {activity && !activity.items.length && <div className="empty"><Activity size={25}/><p>No activity recorded yet.</p></div>}
      </div>
      {activity && <Pager pageNumber={Math.floor(activity.offset / pageSize) + 1} pageSize={pageSize} onSize={changePageSize} hasPrev={activity.offset > 0} hasNext={activity.offset + activity.items.length < activity.total} onPrev={() => setActivityOffset(Math.max(0, activity.offset - pageSize))} onNext={() => setActivityOffset(activity.offset + pageSize)} summary={activity.total ? `Showing ${activity.offset + 1}-${activity.offset + activity.items.length} of ${activity.total.toLocaleString()}` : 'No events'} />}
    </section>
  </>;
}
