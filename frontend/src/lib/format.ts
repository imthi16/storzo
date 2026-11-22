export const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? '' : 's'}`;
export const enc = encodeURIComponent;
export const bytes = (n: number) => n < 1024 ? `${n} B` : n < 1024 ** 2 ? `${(n/1024).toFixed(1)} KB` : n < 1024 ** 3 ? `${(n/1024**2).toFixed(1)} MB` : `${(n/1024**3).toFixed(2)} GB`;
export const isImage = (k: string) => /\.(jpg|jpeg|png|webp|gif|avif)$/i.test(k);
export const shortName = (k: string) => k.split('/').filter(Boolean).pop() || k;
export const folderOf = (k: string) => k.includes('/') ? k.slice(0, k.lastIndexOf('/') + 1) : '';
const pad = (n: number) => String(n).padStart(2, '0');
// Locale-independent local time. 24-hour clock avoids US month/day ambiguity.
export const when = (s: string) => {
  if (!s) return '—';
  const d = new Date(s);
  if (Number.isNaN(d.getTime())) return '—';
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
};

export type AuditSource = 'all' | 'console' | 'gateway';
export type AuditDecision = 'all' | 'allow' | 'deny';
export type GatewayDetail = {method: string; bytes: number; kid: string; decision: 'allow' | 'deny'; status: number};

// Gateway detail is "method=GET bytes=N kid= decision=allow|deny status=NNN".
// A bad string returns null so the row keeps the old action/bucket look.
export const parseGatewayDetail = (detail: string | null | undefined): GatewayDetail | null => {
  if (!detail || typeof detail !== 'string') return null;
  try {
    const map: Record<string, string> = {};
    for (const part of detail.trim().split(/\s+/)) {
      const i = part.indexOf('=');
      if (i <= 0) return null;
      map[part.slice(0, i)] = part.slice(i + 1);
    }
    const method = (map.method ?? '').toUpperCase();
    const decision = map.decision;
    if (!/^[A-Z]+$/.test(method)) return null;
    if (decision !== 'allow' && decision !== 'deny') return null;
    if (!map.status || !/^\d{3}$/.test(map.status)) return null;
    if (map.bytes == null || !/^\d+$/.test(map.bytes)) return null;
    if (!Object.prototype.hasOwnProperty.call(map, 'kid')) return null;
    const kid = map.kid ?? '';
    return {method, bytes: Number(map.bytes), kid, decision, status: Number(map.status)};
  } catch {
    return null;
  }
};

const gatewayVerb = (method: string) => {
  if (method === 'GET' || method === 'HEAD') return 'S3 read';
  if (method === 'PUT' || method === 'POST') return 'S3 write';
  if (method === 'DELETE') return 'S3 delete';
  return 'S3 request';
};

export const activityTitle = (action: string, detail: string | null) => {
  if (action === 'gateway.request') {
    const parsed = parseGatewayDetail(detail);
    if (parsed) return gatewayVerb(parsed.method);
  }
  return action.replaceAll('_', ' ');
};

export const activityRowLabel = (event: {id: number; action: string; detail: string | null; bucket: string | null; object_key: string | null}) => {
  const title = activityTitle(event.action, event.detail);
  const gw = event.action === 'gateway.request' ? parseGatewayDetail(event.detail) : null;
  if (!gw) return `Open event ${event.id}: ${title}`;
  const where = event.object_key ? `${event.bucket || 'console'}/${event.object_key}` : (event.bucket || 'console');
  const decision = gw.decision === 'allow' ? 'allowed' : 'denied';
  return `Open event ${event.id}: ${title}, ${gw.method}, ${decision}, status ${gw.status}, ${where}`;
};

// An empty kid means the gateway did not store one. It stores the kid of every
// request whose signature it verified, including scope denials, so empty means
// authentication failed or the event predates that behavior.
export const GATEWAY_KID_NOTE = 'No credential id was stored: authentication failed, or the event was recorded before the gateway kept the id of authenticated denials.';
export const gatewayKidDisplay = (kid: string): {value: string; note: string | null} =>
  kid ? {value: kid, note: null} : {value: 'not recorded', note: GATEWAY_KID_NOTE};

// The skip link stays an anchor for no-JS, but its href must not become location.hash:
// pageFromHash maps #main-content to the dashboard. Focus main instead.
export const skipToMainContent = (event: {preventDefault: () => void}, doc: Pick<Document, 'getElementById'> = document) => {
  event.preventDefault();
  const main = doc.getElementById('main-content');
  if (!main) return;
  main.focus();
  main.scrollIntoView({block: 'start'});
};

export const auditListQuery = (opts: {limit: number; offset: number; source: AuditSource; decision: AuditDecision}) => {
  const params = new URLSearchParams();
  params.set('limit', String(opts.limit));
  params.set('offset', String(opts.offset));
  params.set('source', opts.source);
  if (opts.source === 'gateway' && (opts.decision === 'allow' || opts.decision === 'deny')) params.set('decision', opts.decision);
  return params.toString();
};

export const activityFilterQuery = (source: AuditSource, decision: AuditDecision) => {
  const params = new URLSearchParams();
  if (source !== 'all') params.set('source', source);
  if (source === 'gateway' && (decision === 'allow' || decision === 'deny')) params.set('decision', decision);
  return params.toString();
};

export const activityFiltersFromHash = (hash: string): {source: AuditSource; decision: AuditDecision} => {
  const q = hash.includes('?') ? hash.slice(hash.indexOf('?') + 1) : '';
  const params = new URLSearchParams(q);
  const source = params.get('source');
  const decision = params.get('decision');
  return {
    source: source === 'console' || source === 'gateway' ? source : 'all',
    decision: decision === 'allow' || decision === 'deny' ? decision : 'all',
  };
};
// Server ceiling is credentials.MAX_TTL (3600). 24 h stays in the list so the limit is visible.
export const CREDENTIAL_TTL_MAX = 3600;
export const CREDENTIAL_TTL_PRESETS = [
  {seconds: 900, label: '15 min'},
  {seconds: 3600, label: '1 h'},
  {seconds: 86400, label: '24 h'},
] as const;
export const ttlPresetEnabled = (seconds: number, max = CREDENTIAL_TTL_MAX) => seconds >= 1 && seconds <= max;

export const prefixError = (prefix: string) => {
  if (!prefix) return 'Enter a prefix that ends with /.';
  if (prefix.startsWith('/') || prefix.includes('\\') || prefix.includes('\0') || prefix.split('/').includes('..')) return "That prefix can't be used. Remove '..', a leading '/' or backslashes.";
  if (new TextEncoder().encode(prefix).length > 1024) return 'That prefix is too long.';
  if (!prefix.endsWith('/')) return 'Prefix must end with /.';
  return '';
};

export type IssuedDetail = {kid: string; mode: 'ro' | 'rw'; exp: number; label: string};

// credential.issue detail: "kid=... mode=ro|rw exp=<unix>" plus optional "label=..." (label may contain spaces).
export const parseIssueDetail = (detail: string | null | undefined): IssuedDetail | null => {
  if (!detail || typeof detail !== 'string') return null;
  const kid = detail.match(/(?:^|\s)kid=(\S+)/)?.[1];
  const mode = detail.match(/(?:^|\s)mode=(ro|rw)(?:\s|$)/)?.[1];
  const expRaw = detail.match(/(?:^|\s)exp=(\d+)/)?.[1];
  if (!kid || (mode !== 'ro' && mode !== 'rw') || !expRaw) return null;
  const label = detail.match(/(?:^|\s)label=(.*)$/)?.[1]?.trim() ?? '';
  return {kid, mode, exp: Number(expRaw), label};
};

export const credentialStatus = (exp: number, nowSec = Date.now() / 1000): 'active' | 'expired' => nowSec > exp ? 'expired' : 'active';

export type CredentialEnv = {
  endpoint: string; accessKeyId: string; secretAccessKey: string; sessionToken: string;
  bucket: string; prefix: string; mode: 'ro' | 'rw';
};

export const credentialEnvText = (c: CredentialEnv) => [
  `STORZO_ENDPOINT=${c.endpoint}`,
  `STORZO_ACCESS_KEY_ID=${c.accessKeyId}`,
  `STORZO_SECRET_ACCESS_KEY=${c.secretAccessKey}`,
  `STORZO_SESSION_TOKEN=${c.sessionToken}`,
  `STORZO_BUCKET=${c.bucket}`,
  `STORZO_PREFIX=${c.prefix}`,
  `STORZO_MODE=${c.mode}`,
].join('\n');

export const credentialMcpJson = (c: CredentialEnv) => JSON.stringify({
  mcpServers: {
    storzo: {
      command: 'storzo-mcp',
      env: {
        STORZO_ENDPOINT: c.endpoint,
        STORZO_ACCESS_KEY_ID: c.accessKeyId,
        STORZO_SECRET_ACCESS_KEY: c.secretAccessKey,
        STORZO_SESSION_TOKEN: c.sessionToken,
        STORZO_BUCKET: c.bucket,
        STORZO_PREFIX: c.prefix,
        STORZO_MODE: c.mode,
      },
    },
  },
}, null, 2);

export const whenShort = (s: string) => s ? new Date(s).toLocaleString(undefined, {month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit'}) : '—';
export const bucketNameError = (n: string) => !n ? '' : n.length < 3 || n.length > 63 ? 'Use 3 to 63 characters.' : /[^a-z0-9.-]/.test(n) ? 'Use only lowercase letters, numbers, hyphens or dots (no spaces, capitals or underscores).' : !/^[a-z0-9].*[a-z0-9]$/.test(n) ? 'Start and end with a letter or number.' : /\.\.|\.-|-\./.test(n) ? 'Do not put dots next to dots or hyphens.' : /^\d+\.\d+\.\d+\.\d+$/.test(n) ? 'Do not use an IP address.' : '';
