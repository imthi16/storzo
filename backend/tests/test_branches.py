import io
from concurrent.futures import ThreadPoolExecutor

import pytest

from app.branches import BranchStore


class Blobs:
    def __init__(self):
        self.objects = {}
        self.calls = []
        self.fail = False

    def put_object(self, *, Bucket, Key, Body):
        self.calls.append(('put', Key))
        if self.fail:
            raise OSError('upload failed')
        self.objects[Bucket, Key] = Body

    def get_object(self, *, Bucket, Key):
        self.calls.append(('get', Key))
        return {'Body': io.BytesIO(self.objects[Bucket, Key])}


def test_fork_and_isolation(tmp_path):
    blobs = Blobs()
    store = BranchStore(tmp_path / 'branches.sqlite', blobs, 'test')
    first = store.put('main', 'a', b'first', {'label': 'original'})
    before = blobs.calls[:]
    store.fork('main', 'trial')
    assert blobs.calls == before
    assert store.list('trial') == {'a': first}
    store.put('trial', 'b', b'first', {'label': 'different'})
    assert len(blobs.objects) == 1
    assert store.list('trial')['b'].metadata == {'label': 'different'}
    store.put('main', 'a', b'main')
    store.put('trial', 'a', b'trial')
    assert store.get('main', 'a') == b'main'
    assert store.get('trial', 'a') == b'trial'
    store.delete('trial', 'a')
    assert store.get('trial', 'b') == b'first'
    assert set(store.list('trial')) == {'b'}
    with pytest.raises(KeyError, match='key'):
        store.get('trial', 'a')
    with pytest.raises(KeyError, match='branch'):
        store.list('missing')
    with pytest.raises(ValueError, match='exists'):
        store.fork('main', 'trial')


def test_failures_and_retry(tmp_path):
    import sqlite3

    path = tmp_path / 'branches.sqlite'
    blobs = Blobs()
    store = BranchStore(path, blobs, 'test')
    store.put('main', 'a', b'old')
    blobs.fail = True
    with pytest.raises(OSError):
        store.put('main', 'a', b'new')
    blobs.fail = False
    with sqlite3.connect(path) as db:
        db.execute("""CREATE TRIGGER fail_entry BEFORE INSERT ON branch_entries
                      BEGIN SELECT RAISE(ABORT, 'injected failure'); END""")
    with pytest.raises(sqlite3.IntegrityError):
        store.put('main', 'a', b'new')
    with pytest.raises(sqlite3.IntegrityError):
        store.fork('main', 'trial')
    store = BranchStore(path, blobs, 'test')
    assert store.get('main', 'a') == b'old'
    with pytest.raises(KeyError):
        store.list('trial')
    with sqlite3.connect(path) as db:
        db.execute('DROP TRIGGER fail_entry')
    store.put('main', 'a', b'new')
    store.fork('main', 'trial')
    assert store.get('trial', 'a') == b'new'


def test_fork_during_write(tmp_path):
    from threading import Event

    uploading = Event()
    resume = Event()

    class PausedBlobs(Blobs):
        def put_object(self, **kwargs):
            if kwargs['Body'] == b'new':
                uploading.set()
                assert resume.wait(5)
            super().put_object(**kwargs)

    store = BranchStore(tmp_path / 'branches.sqlite', PausedBlobs(), 'test')
    store.put('main', 'a', b'old')
    with ThreadPoolExecutor(max_workers=1) as pool:
        pending = pool.submit(store.put, 'main', 'a', b'new')
        try:
            assert uploading.wait(5)
            store.fork('main', 'trial')
        finally:
            resume.set()
        pending.result()
    assert store.get('trial', 'a') == b'old'
    assert store.get('main', 'a') == b'new'
