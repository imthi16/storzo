import { describe, expect, it } from 'vitest';
import { bucketNameError, bytes, folderOf, isImage, plural, shortName } from './format';

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
