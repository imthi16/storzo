import { describe, expect, it } from 'vitest';
import { isActive, progressPercent, toRequest, verifyLabel } from './migrations';

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
describe('toRequest', () => {
  it('maps empty endpoint to null and never trims secrets', () => {
    const r = toRequest({endpoint: ' ', region: '', sourceBucket: ' b ', sourcePrefix: '', accessKeyId: 'AK', secretAccessKey: ' s ', destBucket: 'd', destPrefix: 'x/'});
    expect(r.source.endpoint).toBeNull();
    expect(r.source.region).toBe('us-east-1');
    expect(r.source.bucket).toBe('b');
    expect(r.source.secret_access_key).toBe(' s ');
  });
});
