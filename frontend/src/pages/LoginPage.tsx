import React from 'react';
import { Database, LockKeyhole } from 'lucide-react';

type Props = {
  login: (e: React.FormEvent) => void; username: string; setUsername: (v: string) => void; password: string; setPassword: (v: string) => void;
  loginBusy: boolean; notice: string;
};

export function LoginPage({login, username, setUsername, password, setPassword, loginBusy, notice}: Props) {
  return <div className="login-page"><div className="login-card"><div className="brand-icon large"><Database size={26}/></div><h1>Welcome to <span>Storzo.</span></h1><p>Your images and objects. Your infrastructure. No cloud storage bill.</p><form onSubmit={login}><label>Username<input value={username} onChange={e=>setUsername(e.target.value)} autoComplete="username" required/></label><label>Password<input value={password} onChange={e=>setPassword(e.target.value)} type="password" autoComplete="current-password" required/></label><button className="primary full" disabled={loginBusy}><LockKeyhole size={17}/>{loginBusy?'Signing in…':'Sign in to console'}</button></form><p className="footnote">This console manages the locally hosted SeaweedFS S3 service.</p></div>{notice && <div className="toast" role="status">{notice}</div>}</div>;
}
