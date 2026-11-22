import { describe, expect, it } from 'vitest';
import { isActive, prefixValidationError, progressPercent, toRequest, verifyLabel } from './migrations';

describe('progressPercent', () => {
  it('computes and clamps', () => {
    expect(progressPercent({bytes_done: 5, total_bytes: 10, state: 'running'})).toBe(50);
    expect(progressPercent({bytes_done: 99, total_bytes: 10, state: 'running'})).toBe(100);
    expect(progressPercent({bytes_done: 0, total_bytes: 0, state: 'running'})).toBe(0);
    expect(progressPercent({bytes_done: 0, total_bytes: 0, state: 'done'})).toBe(100);
  });
});
describe('isActive / verifyLabel', () => {
  it('classifies states', () => {
    expect(isActive({state: 'running'})).toBe(true);
    expect(isActive({state: 'done'})).toBe(false);
    expect(verifyLabel({verify_status: 'verified', state: 'done'})).toContain('Verified');
    expect(verifyLabel({verify_status: 'mismatched', state: 'done'})).toContain('Mismatch');
    expect(verifyLabel({verify_status: null, state: 'verifying'})).toContain('Verifying');
  });
});
describe('prefixValidationError', () => {
  const base = {endpoint: '', region: 'us-east-1', sourceBucket: 'b', sourcePrefix: '', accessKeyId: 'AK', secretAccessKey: 's', destBucket: 'd', destPrefix: ''};
  it('rejects partial prefixes and accepts empty or slash-terminated ones', () => {
    expect(prefixValidationError({...base, sourcePrefix: 'logs/2026-'})).toContain('Source prefix');
    expect(prefixValidationError({...base, destPrefix: 'imported'})).toContain('Destination prefix');
    expect(prefixValidationError({...base, sourcePrefix: 'logs/2026/', destPrefix: 'imported/'})).toBeNull();
    expect(prefixValidationError(base)).toBeNull();
  });
});
describe('toRequest', () => {
  it('maps empty endpoint to null and never trims secrets', () => {
    const r = toRequest({endpoint: ' ', region: '', sourceBucket: ' b ', sourcePrefix: '', accessKeyId: 'AK', secretAccessKey: ' s ', destBucket: 'd', destPrefix: 'x/'});
    expect(r.source.endpoint).toBeNull();
    expect(r.source.region).toBe('us-east-1');
    expect(r.source.bucket).toBe('b');
    expect(r.source.secret_access_key).toBe(' s ');
  });
});
