"""SQLite audit trail (single writer, WAL)."""
import sqlite3
from datetime import UTC, datetime, timedelta

from app import config

_COLUMNS = 'id,ts,actor,action,bucket,object_key,detail'


def connect() -> sqlite3.Connection:
    return sqlite3.connect(config.DB_PATH, timeout=10)


def init_db():
    config.DB_PATH.parent.mkdir(parents=True, exist_ok=True)
    with connect() as db:
        db.execute('PRAGMA journal_mode=WAL')
        db.execute('PRAGMA busy_timeout=5000')
        db.execute('''CREATE TABLE IF NOT EXISTS audit (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            ts TEXT NOT NULL, actor TEXT NOT NULL, action TEXT NOT NULL,
            bucket TEXT, object_key TEXT, detail TEXT
        )''')


def audit(action: str, bucket: str | None = None, key: str | None = None, detail: str | None = None):
    with connect() as db:
        db.execute('INSERT INTO audit(ts,actor,action,bucket,object_key,detail) VALUES (?,?,?,?,?,?)',
                   (datetime.now(UTC).isoformat(), config.ADMIN_USER, action, bucket, key, detail))


def list_audit(limit: int, offset: int) -> tuple[list[dict], int]:
    with connect() as db:
        db.row_factory = sqlite3.Row
        total = db.execute('SELECT COUNT(*) FROM audit').fetchone()[0]
        rows = db.execute(f'SELECT {_COLUMNS} FROM audit ORDER BY id DESC LIMIT ? OFFSET ?', (limit, offset)).fetchall()
    return [dict(row) for row in rows], total


def get_audit(audit_id: int) -> dict | None:
    with connect() as db:
        db.row_factory = sqlite3.Row
        row = db.execute(f'SELECT {_COLUMNS} FROM audit WHERE id=?', (audit_id,)).fetchone()
    return dict(row) if row else None


def count_by_day(action: str, days: int) -> dict[str, int]:
    """Events of one action per UTC day over the last `days` days, aggregated in SQL."""
    since = (datetime.now(UTC) - timedelta(days=days - 1)).strftime('%Y-%m-%d')
    with connect() as db:
        rows = db.execute('SELECT substr(ts,1,10) AS day, COUNT(*) FROM audit WHERE action=? AND substr(ts,1,10)>=? GROUP BY day',
                          (action, since)).fetchall()
    return {day: n for day, n in rows}
