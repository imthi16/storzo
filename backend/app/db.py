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


# Spaced so the token cannot match a longer value such as decision=allowed.
# Selected by the validated decision, then bound — never interpolated into SQL text.
_DECISION_TOKEN = {
    'allow': '% decision=allow %',
    'deny': '% decision=deny %',
}


def list_audit(limit: int, offset: int, source: str = 'all', decision: str | None = None) -> tuple[list[dict], int]:
    """Newest-first page. `decision` matches gateway rows only; console + decision is empty."""
    if decision is not None and source == 'console':
        return [], 0
    clauses: list[str] = []
    params: list[str] = []
    if source == 'gateway' or decision is not None:
        clauses.append('action = ?')
        params.append('gateway.request')
    elif source == 'console':
        clauses.append('action != ?')
        params.append('gateway.request')
    if decision is not None:
        clauses.append('detail LIKE ?')
        params.append(_DECISION_TOKEN[decision])
    where = (' WHERE ' + ' AND '.join(clauses)) if clauses else ''
    with connect() as db:
        db.row_factory = sqlite3.Row
        total = db.execute(f'SELECT COUNT(*) FROM audit{where}', params).fetchone()[0]
        rows = db.execute(
            f'SELECT {_COLUMNS} FROM audit{where} ORDER BY id DESC LIMIT ? OFFSET ?',
            (*params, limit, offset),
        ).fetchall()
    return [dict(row) for row in rows], total


def get_audit(audit_id: int) -> dict | None:
    with connect() as db:
        db.row_factory = sqlite3.Row
        row = db.execute(f'SELECT {_COLUMNS} FROM audit WHERE id=?', (audit_id,)).fetchone()
    return dict(row) if row else None


# method=PUT is the first token; the trailing space keeps method=PUTTY from matching.
# ' status=2' is the prefix of a three-digit 2xx status token.
_GATEWAY_PUT = 'method=PUT %'
_GATEWAY_2XX = '% status=2%'


def upload_counts_by_day(days: int) -> dict[str, tuple[int, int]]:
    """UTC-day (console uploads, successful gateway writes) over the last `days` days.

    A gateway write is a gateway.request whose detail has method=PUT and a 2xx status.
    Known limitation: multipart parts are PUT requests too, so a multipart upload counts once per part.
    """
    since = (datetime.now(UTC) - timedelta(days=days - 1)).strftime('%Y-%m-%d')
    with connect() as db:
        rows = db.execute(
            '''SELECT substr(ts,1,10) AS day,
                      SUM(CASE WHEN action = ? THEN 1 ELSE 0 END),
                      SUM(CASE WHEN action = ? AND detail LIKE ? AND detail LIKE ? THEN 1 ELSE 0 END)
               FROM audit WHERE substr(ts,1,10) >= ? GROUP BY day''',
            ('upload', 'gateway.request', _GATEWAY_PUT, _GATEWAY_2XX, since),
        ).fetchall()
    return {day: (int(console), int(gateway)) for day, console, gateway in rows}
