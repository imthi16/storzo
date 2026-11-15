import { afterEach, describe, expect, it, vi } from 'vitest';
import { activityFilterQuery, activityFiltersFromHash, activityRowLabel, activityTitle, auditListQuery, bucketNameError, bytes, folderOf, gatewayKidDisplay, isImage, parseGatewayDetail, plural, shortName, skipToMainContent, when } from './format';
import { pageFromHash } from './routes';

afterEach(() => vi.unstubAllGlobals());

describe('bytes', () => {
  it.each([
    [0, '0 B'],
    [1023, '1023 B'],
    [1024, '1.0 KB'],
    [1024 ** 2, '1.0 MB'],
    [1024 ** 3, '1.00 GB'],
  ])('formats %d as %s', (n, out) => expect(bytes(n)).toBe(out));
});

describe('plural', () => {
  it('only drops the s for exactly one', () => {
    expect(plural(1, 'file')).toBe('1 file');
    expect(plural(0, 'file')).toBe('0 files');
    expect(plural(2, 'file')).toBe('2 files');
  });
});

describe('object keys', () => {
  it('shortName returns the last path segment', () => {
    expect(shortName('a/b/c.png')).toBe('c.png');
    expect(shortName('a/b/')).toBe('b');
  });
  it('folderOf keeps the trailing slash and is empty at the root', () => {
    expect(folderOf('a/b/c.png')).toBe('a/b/');
    expect(folderOf('c.png')).toBe('');
  });
  it('isImage is case-insensitive and extension-only', () => {
    expect(isImage('x.JPG')).toBe(true);
    expect(isImage('x.png.txt')).toBe(false);
  });
});

describe('when', () => {
  it('uses a locale-independent 24-hour local timestamp', () => {
    const local = new Date(2026, 10, 15, 17, 14, 20);
    expect(when(local.toISOString())).toBe('2026-11-15 17:14:20');
  });
  it('uses an em dash for empty or unparseable input', () => {
    expect(when('')).toBe('—');
    expect(when('not-a-date')).toBe('—');
  });
});

describe('parseGatewayDetail', () => {
  it('parses an allow row and an empty-kid denial', () => {
    expect(parseGatewayDetail('method=PUT bytes=12 kid=STZAAAAAAAAAAAAAAAA decision=allow status=200')).toEqual({
      method: 'PUT', bytes: 12, kid: 'STZAAAAAAAAAAAAAAAA', decision: 'allow', status: 200,
    });
    expect(parseGatewayDetail('method=GET bytes=0 kid= decision=deny status=403')).toEqual({
      method: 'GET', bytes: 0, kid: '', decision: 'deny', status: 403,
    });
  });
  it('returns null for malformed detail and does not throw', () => {
    expect(parseGatewayDetail(null)).toBeNull();
    expect(parseGatewayDetail('')).toBeNull();
    expect(parseGatewayDetail('not a detail')).toBeNull();
    expect(parseGatewayDetail('method=GET bytes=no kid= decision=deny status=403')).toBeNull();
    expect(parseGatewayDetail('method=GET bytes=1 kid=x decision=maybe status=200')).toBeNull();
    expect(() => parseGatewayDetail('= = =')).not.toThrow();
  });
});

describe('activityTitle', () => {
  const detail = (method: string) => `method=${method} bytes=1 kid=k decision=allow status=200`;
  it('names a gateway call without repeating the method chip', () => {
    expect(activityTitle('gateway.request', detail('GET'))).toBe('S3 read');
    expect(activityTitle('gateway.request', detail('head'))).toBe('S3 read');
    expect(activityTitle('gateway.request', detail('PUT'))).toBe('S3 write');
    expect(activityTitle('gateway.request', detail('POST'))).toBe('S3 write');
    expect(activityTitle('gateway.request', detail('DELETE'))).toBe('S3 delete');
    expect(activityTitle('gateway.request', detail('COPY'))).toBe('S3 request');
  });
  it('keeps the spaced action when the detail is not a gateway row', () => {
    expect(activityTitle('create_bucket', null)).toBe('create bucket');
    expect(activityTitle('gateway.request', 'not a detail')).toBe('gateway.request');
  });
});

describe('activityRowLabel', () => {
  it('includes method, decision, status and location for an allow', () => {
    expect(activityRowLabel({
      id: 42, action: 'gateway.request', bucket: 'agent-scratch', object_key: 'run-42/a.txt',
      detail: 'method=GET bytes=1 kid=k decision=allow status=200',
    })).toBe('Open event 42: S3 read, GET, allowed, status 200, agent-scratch/run-42/a.txt');
  });
  it('spells a deny as denied', () => {
    expect(activityRowLabel({
      id: 7, action: 'gateway.request', bucket: 'agent-scratch', object_key: 'x',
      detail: 'method=PUT bytes=0 kid= decision=deny status=403',
    })).toBe('Open event 7: S3 write, PUT, denied, status 403, agent-scratch/x');
  });
  it('keeps the old label when gateway detail does not parse', () => {
    expect(activityRowLabel({
      id: 3, action: 'gateway.request', bucket: 'agent-scratch', object_key: 'x', detail: 'not a detail',
    })).toBe('Open event 3: gateway.request');
  });
  it('keeps the old label for a console row', () => {
    expect(activityRowLabel({
      id: 4, action: 'create_bucket', bucket: null, object_key: null, detail: 'name=demo',
    })).toBe('Open event 4: create bucket');
  });
});

describe('gatewayKidDisplay', () => {
  it('uses a neutral label when the gateway stored no credential id', () => {
    expect(gatewayKidDisplay('')).toEqual({
      value: 'not recorded',
      note: 'No credential id was stored: authentication failed, or the event was recorded before the gateway kept the id of authenticated denials.',
    });
  });
  it('shows a stored id without the empty note', () => {
    expect(gatewayKidDisplay('STZAAAAAAAAAAAAAAAA')).toEqual({value: 'STZAAAAAAAAAAAAAAAA', note: null});
  });
});

describe('skipToMainContent', () => {
  it('focuses main and does not write location.hash', () => {
    const hash = {value: '#/activity'};
    const locationLike = {};
    Object.defineProperty(locationLike, 'hash', {get: () => hash.value, set: (v: string) => { hash.value = v; }});
    const main = {focus: vi.fn(), scrollIntoView: vi.fn()};
    const event = {preventDefault: vi.fn()};
    skipToMainContent(event, {getElementById: (id) => id === 'main-content' ? main as unknown as HTMLElement : null});
    expect(event.preventDefault).toHaveBeenCalledOnce();
    expect(main.focus).toHaveBeenCalledOnce();
    expect(main.scrollIntoView).toHaveBeenCalledWith({block: 'start'});
    expect(hash.value).toBe('#/activity');
  });
  it('still cancels navigation when main is missing', () => {
    const event = {preventDefault: vi.fn()};
    expect(() => skipToMainContent(event, {getElementById: () => null})).not.toThrow();
    expect(event.preventDefault).toHaveBeenCalledOnce();
  });
  it('shows why the href must be cancelled: #main-content is not the activity route', () => {
    vi.stubGlobal('location', {hash: '#main-content'});
    expect(pageFromHash()).toEqual({page: 'dashboard', id: null});
    vi.stubGlobal('location', {hash: '#/activity'});
    expect(pageFromHash()).toEqual({page: 'activity', id: null});
  });
});

describe('audit list query', () => {
  it('sends source and only sends decision for a gateway allow or deny', () => {
    expect(auditListQuery({limit: 10, offset: 0, source: 'all', decision: 'all'})).toBe('limit=10&offset=0&source=all');
    expect(auditListQuery({limit: 10, offset: 20, source: 'console', decision: 'deny'})).toBe('limit=10&offset=20&source=console');
    expect(auditListQuery({limit: 25, offset: 0, source: 'gateway', decision: 'deny'})).toBe('limit=25&offset=0&source=gateway&decision=deny');
  });
  it('keeps non-default filters in a hash query that pageFromHash still reads as activity', () => {
    expect(activityFilterQuery('all', 'deny')).toBe('');
    expect(activityFilterQuery('gateway', 'allow')).toBe('source=gateway&decision=allow');
    expect(activityFiltersFromHash('#/activity/?source=gateway&decision=deny')).toEqual({source: 'gateway', decision: 'deny'});
    expect(activityFiltersFromHash('#/activity')).toEqual({source: 'all', decision: 'all'});
    vi.stubGlobal('location', {hash: '#/activity/?source=gateway&decision=allow'});
    expect(pageFromHash()).toEqual({page: 'activity', id: null});
  });
});

describe('bucketNameError', () => {
  it('accepts a valid name and ignores empty input', () => {
    expect(bucketNameError('')).toBe('');
    expect(bucketNameError('demo-images')).toBe('');
  });
  it.each([
    ['ab', /3 to 63/],
    ['Demo', /lowercase/],
    ['-demo', /Start and end/],
    ['de..mo', /dots/],
    ['192.168.0.1', /IP address/],
  ])('rejects %s', (name, msg) => expect(bucketNameError(name)).toMatch(msg));
});
