export const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? '' : 's'}`;
export const enc = encodeURIComponent;
export const bytes = (n: number) => n < 1024 ? `${n} B` : n < 1024 ** 2 ? `${(n/1024).toFixed(1)} KB` : n < 1024 ** 3 ? `${(n/1024**2).toFixed(1)} MB` : `${(n/1024**3).toFixed(2)} GB`;
export const isImage = (k: string) => /\.(jpg|jpeg|png|webp|gif|avif)$/i.test(k);
export const shortName = (k: string) => k.split('/').filter(Boolean).pop() || k;
export const folderOf = (k: string) => k.includes('/') ? k.slice(0, k.lastIndexOf('/') + 1) : '';
export const when = (s: string) => s ? new Date(s).toLocaleString() : '—';
export const whenShort = (s: string) => s ? new Date(s).toLocaleString(undefined, {month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit'}) : '—';
export const bucketNameError = (n: string) => !n ? '' : n.length < 3 || n.length > 63 ? 'Use 3 to 63 characters.' : /[^a-z0-9.-]/.test(n) ? 'Use only lowercase letters, numbers, hyphens or dots (no spaces, capitals or underscores).' : !/^[a-z0-9].*[a-z0-9]$/.test(n) ? 'Start and end with a letter or number.' : /\.\.|\.-|-\./.test(n) ? 'Do not put dots next to dots or hyphens.' : /^\d+\.\d+\.\d+\.\d+$/.test(n) ? 'Do not use an IP address.' : '';
