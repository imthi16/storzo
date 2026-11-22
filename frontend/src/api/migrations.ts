import { call } from './client';

export type MigrationState = 'queued' | 'running' | 'verifying' | 'done' | 'failed' | 'cancelled';
export type MigrationJob = {
  id: string; state: MigrationState; verify_status: 'verified' | 'mismatched' | 'failed' | null;
  source_bucket: string; source_prefix: string; source_endpoint: string | null; source_region: string | null;
  dest_bucket: string; dest_prefix: string;
  bytes_done: number; objects_done: number; total_bytes: number; total_objects: number;
  errors: string[]; created_at: string; updated_at: string;
};
export type Estimate = {
  object_count: number; total_bytes: number; partial: boolean; scan_limit: number;
  egress_usd: number; egress_price_per_gb_usd: number; price_note: string;
};
export type MigrationForm = {
  endpoint: string; region: string; sourceBucket: string; sourcePrefix: string;
  accessKeyId: string; secretAccessKey: string; destBucket: string; destPrefix: string;
};

export const progressPercent = (job: Pick<MigrationJob, 'bytes_done' | 'total_bytes' | 'state'>): number => {
  if (job.state === 'done') return 100;
  if (!job.total_bytes) return 0;
  return Math.min(100, Math.round((job.bytes_done / job.total_bytes) * 100));
};

export const isActive = (job: Pick<MigrationJob, 'state'>): boolean =>
  job.state === 'queued' || job.state === 'running' || job.state === 'verifying';

export const verifyLabel = (job: Pick<MigrationJob, 'verify_status' | 'state'>): string => {
  if (job.verify_status === 'verified') return 'Verified — source and destination match (rclone check --one-way)';
  if (job.verify_status === 'mismatched') return 'Mismatch — rclone check found differences';
  if (job.verify_status === 'failed') return 'Verification failed';
  return job.state === 'verifying' ? 'Verifying…' : 'Not verified yet';
};

export const toRequest = (f: MigrationForm) => ({
  source: {
    endpoint: f.endpoint.trim() || null, region: f.region.trim() || 'us-east-1',
    bucket: f.sourceBucket.trim(), prefix: f.sourcePrefix.trim(),
    access_key_id: f.accessKeyId, secret_access_key: f.secretAccessKey,
  },
  destination: {bucket: f.destBucket.trim(), prefix: f.destPrefix.trim()},
});

export const estimateMigration = (csrf: string, f: MigrationForm) =>
  call<Estimate>('/migrations/estimate', csrf, {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(toRequest(f))});
export const startMigration = (csrf: string, f: MigrationForm) =>
  call<MigrationJob>('/migrations', csrf, {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(toRequest(f))});
export const listMigrations = () => call<{items: MigrationJob[]}>('/migrations');
export const cancelMigration = (csrf: string, id: string) =>
  call<MigrationJob>(`/migrations/${encodeURIComponent(id)}/cancel`, csrf, {method: 'POST'});
