import type { Audit, Page } from '../types';
import { activityTitle } from '../lib/format';
import { ActivityDetail } from '../components/ActivityDetail';

type Props = {
  entry: Audit | null; entryError: string; activityId: number | null; nav: (p: Page) => void;
  openLocation: (bucket: string, key: string | null) => void;
};

export function ActivityEventPage({entry, entryError, activityId, nav, openLocation}: Props) {
  return <>
      <div className="heading"><div><h1>Event #{activityId}</h1><p>{entry?`${activityTitle(entry.action, entry.detail)}${entry.bucket?` on ${entry.bucket}`:''}`:'Loading event details'}</p></div></div><ActivityDetail entry={entry} error={entryError} onBack={()=>nav('activity')} onOpenLocation={openLocation}/>
  </>;
}
