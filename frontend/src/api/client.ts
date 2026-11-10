/** An API failure whose message is already written for the user: what happened and what to do next. */
export class ApiError extends Error {
  constructor(public status: number, public detail: string, message: string, public retryable: boolean) { super(message); }
  get isSession() { return this.status === 401 && this.detail !== 'Invalid credentials'; }
}

export function explain(status: number, detail: string): {message: string; retryable: boolean} {
  const d = detail.toLowerCase();
  if (status === 0) return {message: "Can't reach Storzo. Check your connection, then try again.", retryable: true};
  if (status === 401) return d.includes('invalid credentials')
    ? {message: 'The username or password is wrong. Check both and try again.', retryable: false}
    : {message: 'Your session has ended. Sign in again to continue.', retryable: false};
  if (status === 403) return d.includes('csrf')
    ? {message: 'This page is out of date. Refresh it, then try again.', retryable: false}
    : {message: "Storage refused the request. Check that the S3 access key in .env matches the storage service.", retryable: false};
  if (status === 404) return {message: 'That bucket or file no longer exists. Refresh the list to see what is there now.', retryable: false};
  if (status === 409) return d.includes('not empty')
    ? {message: 'This bucket still contains files. Delete them first, then delete the bucket.', retryable: false}
    : {message: 'A bucket with that name already exists. Choose a different name.', retryable: false};
  if (status === 413) { const limit = detail.match(/(\d+)\s*MB/i); return {message: limit ? `This file is larger than the ${limit[1]} MB upload limit. Choose a smaller file or compress it.` : 'This file is too large. Choose a smaller file or compress it.', retryable: false}; }
  if (status === 415) return d.includes('executable')
    ? {message: 'SVG, HTML and JavaScript files cannot be uploaded here because a browser could run them. Convert the image to PNG or JPG, or put the file in a zip.', retryable: false}
    : {message: "This file type isn't supported. Upload a JPG, PNG, WebP, GIF or AVIF image, or another document type.", retryable: false};
  if (status === 400 && d.includes('bucket name')) return {message: 'Use 3 to 63 lowercase letters, numbers, hyphens or dots, starting and ending with a letter or number.', retryable: false};
  if (status === 400 && d.includes('object key')) return {message: "That file name can't be used. Remove '..', a leading '/' or backslashes, then rename the file.", retryable: false};
  if (status === 400 && d.includes('empty')) return {message: 'This file is empty. Choose a file that has content.', retryable: false};
  if (status === 503) return {message: 'Storage is not responding. Wait a moment and try again. If it keeps failing, check that the storage service is running.', retryable: true};
  if (status >= 500) return {message: 'Something went wrong on the server. Try again in a moment.', retryable: true};
  return {message: detail ? `${detail.replace(/\.$/, '')}. Check the details and try again.` : 'The request could not be completed. Try again.', retryable: false};
}

export function apiError(status: number, body: string): ApiError {
  let detail = '';
  try { const j = JSON.parse(body); if (typeof j.detail === 'string') detail = j.detail; } catch { /* not JSON */ }
  const {message, retryable} = explain(status, detail);
  return new ApiError(status, detail, message, retryable);
}

export async function call<T>(path: string, csrf?: string, options: RequestInit = {}): Promise<T> {
  const method = (options.method || 'GET').toUpperCase();
  const headers = new Headers(options.headers);
  if (csrf && !['GET', 'HEAD', 'OPTIONS'].includes(method)) headers.set('X-CSRF-Token', csrf);
  let res: Response;
  try {
    res = await fetch('/api' + path, {...options, credentials: 'same-origin', headers});
  } catch {
    throw new ApiError(0, '', explain(0, '').message, true);
  }
  if (!res.ok) throw apiError(res.status, await res.text().catch(() => ''));
  return res.json() as Promise<T>;
}
