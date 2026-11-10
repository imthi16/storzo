import { ApiError, apiError, explain } from './client';

/** Upload one file with progress events (fetch cannot report upload progress). */
export function postUpload(bucket: string, key: string, file: File, csrf: string, onProgress: (pct: number) => void): Promise<void> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open('POST', '/api/objects/upload');
    xhr.withCredentials = true;
    xhr.setRequestHeader('X-CSRF-Token', csrf);
    xhr.upload.onprogress = e => { if (e.lengthComputable) onProgress(Math.round(e.loaded / e.total * 100)); };
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) { onProgress(100); resolve(); return; }
      reject(apiError(xhr.status, xhr.responseText));
    };
    xhr.onerror = () => reject(new ApiError(0, '', explain(0, '').message, true));
    xhr.ontimeout = () => reject(new ApiError(0, '', 'The upload timed out. Check your connection, then retry.', true));
    const data = new FormData();
    data.append('bucket', bucket); data.append('key', key); data.append('file', file);
    xhr.send(data);
  });
}
