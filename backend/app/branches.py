"""Internal single-bucket copy-on-write branch store for the proof of concept."""

import hashlib
import json
import sqlite3
from collections.abc import Iterator, Mapping
from contextlib import closing, contextmanager
from dataclasses import dataclass
from pathlib import Path
from typing import Any


@dataclass(frozen=True)
class Entry:
    sha256: str
    size: int
    metadata: dict[str, str]


class BranchStore:
    """Owns the manifest database and uses a dedicated bucket for immutable blobs."""

    def __init__(self, path: Path, s3: Any, bucket: str) -> None:
        self.path = path
        self.s3 = s3
        self.bucket = bucket
        with closing(sqlite3.connect(path)) as db, db:
            db.executescript("""
                CREATE TABLE IF NOT EXISTS branches (name TEXT PRIMARY KEY);
                CREATE TABLE IF NOT EXISTS branch_blobs (sha256 TEXT PRIMARY KEY);
                CREATE TABLE IF NOT EXISTS branch_entries (
                    branch TEXT NOT NULL REFERENCES branches(name),
                    key TEXT NOT NULL,
                    sha256 TEXT NOT NULL REFERENCES branch_blobs(sha256),
                    size INTEGER NOT NULL,
                    metadata TEXT NOT NULL,
                    PRIMARY KEY (branch, key)
                );
                INSERT OR IGNORE INTO branches VALUES ('main');
            """)

    @contextmanager
    def _transaction(self, *, write: bool = False) -> Iterator[sqlite3.Connection]:
        with closing(sqlite3.connect(self.path)) as db, db:
            db.execute('PRAGMA foreign_keys = ON')
            db.execute('BEGIN IMMEDIATE' if write else 'BEGIN')
            yield db

    @staticmethod
    def _require_branch(db: sqlite3.Connection, branch: str) -> None:
        if db.execute('SELECT 1 FROM branches WHERE name = ?', (branch,)).fetchone() is None:
            raise KeyError(f'branch {branch!r} does not exist')

    def fork(self, source: str, target: str) -> None:
        with self._transaction(write=True) as db:
            self._require_branch(db, source)
            created = db.execute('INSERT OR IGNORE INTO branches VALUES (?)', (target,))
            if created.rowcount == 0:
                raise ValueError(f'branch {target!r} already exists')
            db.execute("""
                INSERT INTO branch_entries (branch, key, sha256, size, metadata)
                SELECT ?, key, sha256, size, metadata FROM branch_entries WHERE branch = ?
            """, (target, source))

    def put(self, branch: str, key: str, body: bytes,
            metadata: Mapping[str, str] | None = None) -> Entry:
        entry = Entry(hashlib.sha256(body).hexdigest(), len(body), dict(metadata or {}))
        encoded_metadata = json.dumps(entry.metadata)
        with self._transaction() as db:
            self._require_branch(db, branch)
            stored = db.execute(
                'SELECT 1 FROM branch_blobs WHERE sha256 = ?', (entry.sha256,),
            ).fetchone()
        # Concurrent uploads of the same digest write the same bytes.
        if stored is None:
            self.s3.put_object(Bucket=self.bucket, Key=entry.sha256, Body=body)
        with self._transaction(write=True) as db:
            db.execute('INSERT OR IGNORE INTO branch_blobs VALUES (?)', (entry.sha256,))
            db.execute("""
                INSERT INTO branch_entries VALUES (?, ?, ?, ?, ?)
                ON CONFLICT(branch, key) DO UPDATE SET
                    sha256 = excluded.sha256, size = excluded.size, metadata = excluded.metadata
            """, (branch, key, entry.sha256, entry.size, encoded_metadata))
        return entry

    def get(self, branch: str, key: str) -> bytes:
        with self._transaction() as db:
            self._require_branch(db, branch)
            row = db.execute(
                'SELECT sha256 FROM branch_entries WHERE branch = ? AND key = ?', (branch, key),
            ).fetchone()
            if row is None:
                raise KeyError(f'key {key!r} does not exist in branch {branch!r}')
        with closing(self.s3.get_object(Bucket=self.bucket, Key=row[0])['Body']) as body:
            data: bytes = body.read()
        return data

    def list(self, branch: str) -> dict[str, Entry]:
        with self._transaction() as db:
            self._require_branch(db, branch)
            rows = db.execute(
                'SELECT key, sha256, size, metadata FROM branch_entries WHERE branch = ? ORDER BY key',
                (branch,),
            )
            return {key: Entry(digest, size, json.loads(metadata)) for key, digest, size, metadata in rows}

    def delete(self, branch: str, key: str) -> None:
        with self._transaction(write=True) as db:
            self._require_branch(db, branch)
            deleted = db.execute(
                'DELETE FROM branch_entries WHERE branch = ? AND key = ?', (branch, key),
            )
            if deleted.rowcount == 0:
                raise KeyError(f'key {key!r} does not exist in branch {branch!r}')
