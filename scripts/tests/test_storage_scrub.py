"""Unit tests for scripts/storage_scrub.py comparison/walk/state logic.

No Docker, no network, no app imports: a fake S3 client stands in for boto3.
Run: python3 -m pytest scripts/tests -q
"""
import hashlib
import io
import json
import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import storage_scrub as scrub  # noqa: E402


class FakeBody:
    def __init__(self, data):
        self._io = io.BytesIO(data)

    def read(self, size=-1):
        return self._io.read(size)


class FakeS3:
    """objects: {bucket: {key: (data, metadata_dict, etag)}}; pages of 2 keys."""

    def __init__(self, objects):
        self.objects = objects
        self.fail_head = set()
        self.fail_get = set()
        self.change_on_get = set()

    def list_objects_v2(self, Bucket, StartAfter=None, ContinuationToken=None):
        keys = sorted(self.objects.get(Bucket, {}))
        if StartAfter:
            keys = [k for k in keys if k > StartAfter]
        start = int(ContinuationToken) if ContinuationToken else 0
        page = keys[start:start + 2]
        resp = {'Contents': [{'Key': k} for k in page]}
        if start + 2 < len(keys):
            resp['IsTruncated'] = True
            resp['NextContinuationToken'] = str(start + 2)
        return resp

    def head_object(self, Bucket, Key):
        if (Bucket, Key) in self.fail_head:
            raise RuntimeError('head boom')
        _, meta, etag = self.objects[Bucket][Key]
        return {'Metadata': dict(meta), 'ETag': etag}

    def get_object(self, Bucket, Key):
        if (Bucket, Key) in self.fail_get:
            raise RuntimeError('get boom')
        data, _, etag = self.objects[Bucket][Key]
        if (Bucket, Key) in self.change_on_get:
            etag = etag + '-changed'
        return {'Body': FakeBody(data), 'ETag': etag}


def obj(data=b'hello', sha=None, etag='"e1"'):
    meta = {'sha256': sha} if sha is not None else {}
    return (data, meta, etag)


def digest(data):
    return hashlib.sha256(data).hexdigest()


class TestClassify:
    def test_match(self):
        assert scrub.classify(digest(b'x'), digest(b'x')) == 'match'

    def test_match_normalizes_case_and_whitespace(self):
        assert scrub.classify('  ' + digest(b'x').upper() + '\n', digest(b'x')) == 'match'

    def test_mismatch(self):
        assert scrub.classify(digest(b'x'), digest(b'y')) == 'mismatch'

    @pytest.mark.parametrize('stored', [None, '', '   '])
    def test_no_metadata(self, stored):
        assert scrub.classify(stored, digest(b'x')) == 'no-metadata'


class TestSha256Stream:
    def test_known_vector(self):
        assert scrub.sha256_stream(FakeBody(b'abc')) == (
            'ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad')

    def test_chunked_reads(self):
        data = b'z' * 10000
        assert scrub.sha256_stream(FakeBody(data), chunk_size=7) == digest(data)

    def test_empty(self):
        assert scrub.sha256_stream(FakeBody(b'')) == digest(b'')


class TestIterKeys:
    def test_pagination_and_order(self):
        s3 = FakeS3({'b': {f'k{i}': obj() for i in range(5)}})
        assert list(scrub.iter_keys(s3, 'b')) == [f'k{i}' for i in range(5)]

    def test_start_after_skips(self):
        s3 = FakeS3({'b': {f'k{i}': obj() for i in range(5)}})
        assert list(scrub.iter_keys(s3, 'b', 'k2')) == ['k3', 'k4']

    def test_empty_bucket(self):
        assert list(scrub.iter_keys(FakeS3({}), 'b')) == []


class TestCheckOne:
    def test_match(self):
        s3 = FakeS3({'b': {'k': obj(b'data', digest(b'data'))}})
        assert scrub.check_one(s3, 'b', 'k')[0] == 'match'

    def test_match_with_capitalized_key(self):
        # SeaweedFS HEAD returns "Sha256", not "sha256" (observed live on 4.53).
        s3 = FakeS3({'b': {'k': (b'data', {'Sha256': digest(b'data')}, '"e1"')}})
        assert scrub.check_one(s3, 'b', 'k')[0] == 'match'

    def test_mismatch(self):
        s3 = FakeS3({'b': {'k': obj(b'data', digest(b'other'))}})
        assert scrub.check_one(s3, 'b', 'k')[0] == 'mismatch'

    def test_no_metadata_skips_download(self):
        s3 = FakeS3({'b': {'k': obj(b'data')}})
        s3.fail_get.add(('b', 'k'))  # would raise if downloaded
        assert scrub.check_one(s3, 'b', 'k')[0] == 'no-metadata'

    def test_head_failure(self):
        s3 = FakeS3({'b': {'k': obj()}})
        s3.fail_head.add(('b', 'k'))
        cat, detail = scrub.check_one(s3, 'b', 'k')
        assert cat == 'unreadable' and 'HEAD' in detail

    def test_get_failure(self):
        s3 = FakeS3({'b': {'k': obj(b'data', digest(b'data'))}})
        s3.fail_get.add(('b', 'k'))
        assert scrub.check_one(s3, 'b', 'k')[0] == 'unreadable'

    def test_changed_between_head_and_get(self):
        s3 = FakeS3({'b': {'k': obj(b'data', digest(b'data'))}})
        s3.change_on_get.add(('b', 'k'))
        cat, detail = scrub.check_one(s3, 'b', 'k')
        assert cat == 'unreadable' and 'changed' in detail


class TestState:
    def test_roundtrip(self, tmp_path):
        path = tmp_path / 'state.json'
        state = scrub.State(path)
        state.done['b'] = 'k9'
        state.counts['match'] = 3
        state.save()
        loaded = scrub.State(path)
        assert loaded.done == {'b': 'k9'}
        assert loaded.counts['match'] == 3
        assert loaded.counts['mismatch'] == 0

    def test_no_path_is_noop(self):
        state = scrub.State(None)
        state.save()  # must not raise

    def test_bad_version_rejected(self, tmp_path):
        path = tmp_path / 'state.json'
        path.write_text(json.dumps({'version': 99}))
        with pytest.raises(ValueError):
            scrub.State(path)


class TestRunScrub:
    def test_counts_and_problems(self):
        s3 = FakeS3({'b': {
            'ok': obj(b'a', digest(b'a')),
            'bad': obj(b'a', digest(b'b')),
            'nometa': obj(b'a'),
        }})
        s3.fail_head.add(('b', 'gone'))
        s3.objects['b']['gone'] = obj()
        state = scrub.State(None)
        result = scrub.run_scrub(s3, ['b'], state)
        assert result['counts'] == {'match': 1, 'mismatch': 1, 'no-metadata': 1, 'unreadable': 1}
        assert result['processed'] == 4 and not result['bounded']
        assert {p['key'] for p in result['problems']} == {'bad', 'gone'}

    def test_bounded_then_resume(self, tmp_path):
        s3 = FakeS3({'b': {f'k{i}': obj(b'a', digest(b'a')) for i in range(5)}})
        path = tmp_path / 'state.json'
        first = scrub.run_scrub(s3, ['b'], scrub.State(path), max_objects=2)
        assert first['processed'] == 2 and first['bounded']
        assert first['counts']['match'] == 2
        second = scrub.run_scrub(s3, ['b'], scrub.State(path), max_objects=2)
        assert second['processed'] == 2 and second['counts']['match'] == 4
        third = scrub.run_scrub(s3, ['b'], scrub.State(path))
        assert third['processed'] == 1 and not third['bounded']
        assert third['counts']['match'] == 5
        assert scrub.State(path).done['b'] == 'k4'

    def test_multiple_buckets(self):
        s3 = FakeS3({'b1': {'k': obj(b'a', digest(b'a'))},
                     'b2': {'k': obj(b'a', digest(b'wrong'))}})
        result = scrub.run_scrub(s3, ['b1', 'b2'], scrub.State(None))
        assert result['counts']['match'] == 1
        assert result['counts']['mismatch'] == 1


class TestLoadEnv:
    def test_does_not_override_real_env(self, tmp_path, monkeypatch):
        env = tmp_path / '.env'
        env.write_text('S3_ACCESS_KEY=fromfile\n# comment\nOTHER=x\n')
        monkeypatch.setenv('S3_ACCESS_KEY', 'fromenv')
        monkeypatch.delenv('OTHER', raising=False)
        scrub.load_env(env)
        import os
        assert os.environ['S3_ACCESS_KEY'] == 'fromenv'
        assert os.environ['OTHER'] == 'x'

    def test_missing_file_is_noop(self, tmp_path):
        scrub.load_env(tmp_path / 'absent')
