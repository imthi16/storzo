import { CheckCircle2 } from 'lucide-react';

type Props = {ready: 'checking' | 'online' | 'offline'};

export function SettingsPage({ready}: Props) {
  return <>
      <div className="heading"><div><h1>Settings & deployment</h1><p>Instance configuration and operational guidelines.</p></div></div><section className="panel setting-panel"><h2>Current installation</h2><div className="health-item"><span>Storage engine</span><strong>SeaweedFS (S3 gateway)</strong></div><div className="health-item"><span>Management backend</span><strong>FastAPI</strong></div><div className="health-item"><span>Storage mode</span><strong>On-premises</strong></div><div className="health-item"><span>Health</span><strong className={ready==='online'?'good':'bad'}>{ready==='online'?'Connected':'Unavailable'}</strong></div><p className="panel-note">S3 credentials, admin credentials, disk and PVC size are managed through Docker/Kubernetes environment secrets. This UI intentionally never reveals secrets.</p></section><section className="panel setting-panel"><h2>Production checklist</h2>{['Configure TLS and secure cookies','Rotate generated S3/admin credentials','Use persistent Kubernetes volumes','Configure tested off-host backups','Enable network policies and rate limiting','Run S3 SDK compatibility and restore tests'].map(x=><div className="check-row" key={x}><CheckCircle2 size={18}/>{x}</div>)}</section>
  </>;
}
