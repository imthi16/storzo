"""Stateless scoped agent credentials (spec: docs/SPEC-agent-gateway.md).

access_key_id     = kid
secret_access_key = hex(HMAC(key, b"secret|" + kid))
session_token     = b64url(claims JSON) + "." + b64url(HMAC(key, <the b64url claims segment, as sent>))

The gateway recomputes the secret from kid and verifies the token, so nothing is stored.
"""
import base64
import hashlib
import hmac
import json
import re
import secrets
import time
import uuid
from contextlib import closing

from app import config, db

MAX_TTL = 3600
SKEW = 60
# Keep a revocation through token expiry leeway plus backend/gateway clock skew.
_REVOCATION_LEEWAY = 2 * SKEW
_KID = re.compile(r'STZ[A-Z2-7]{16}')


def _b64(raw: bytes) -> str:
    return base64.urlsafe_b64encode(raw).rstrip(b'=').decode()


def _unb64(text: str) -> bytes:
    return base64.urlsafe_b64decode(text + '=' * (-len(text) % 4))


def _mac(msg: bytes) -> bytes:
    return hmac.new(config.CRED_SIGNING_KEY.encode(), msg, hashlib.sha256).digest()


def issue(bucket: str, prefix: str, mode: str, ttl: int, now: int | None = None) -> dict:
    if len(config.CRED_SIGNING_KEY) < 32:
        raise RuntimeError('CRED_SIGNING_KEY must have at least 32 characters')
    iat = int(time.time()) if now is None else now
    kid = 'STZ' + base64.b32encode(secrets.token_bytes(10)).decode().rstrip('=')
    claims = {'kid': kid, 'bucket': bucket, 'prefix': prefix, 'mode': mode,
              'iat': iat, 'exp': iat + ttl, 'jti': str(uuid.uuid4())}
    body = _b64(json.dumps(claims, separators=(',', ':'), sort_keys=True).encode())
    return {'access_key_id': kid,
            'secret_access_key': _mac(b'secret|' + kid.encode()).hex(),
            'session_token': body + '.' + _b64(_mac(body.encode())),
            'expires_at': claims['exp'], 'claims': claims}


def verify(token: str, now: int | None = None) -> dict:
    """Return the claims or raise ValueError. Used by tests and as the reference for the Go gateway."""
    body, _, sig = token.partition('.')
    if not hmac.compare_digest(_b64(_mac(body.encode())), sig):
        raise ValueError('bad signature')
    claims = json.loads(_unb64(body))
    if (int(time.time()) if now is None else now) > claims['exp'] + SKEW:
        raise ValueError('expired')
    return claims


def revoke(kid: str) -> bool:
    if not _KID.fullmatch(kid):
        return False
    with closing(db.connect()) as conn, conn:
        row = conn.execute(
            "SELECT bucket, object_key, detail FROM audit WHERE action = 'credential.issue' AND detail GLOB ? "
            "ORDER BY id DESC LIMIT 1", (f'kid={kid} mode=*',),
        ).fetchone()
        if row is None:
            return False
        bucket, prefix, detail = row
        # The expiry precedes the optional label, which can contain arbitrary text.
        expires_at = int(detail.split(' ', 3)[2].removeprefix('exp='))
        now = int(time.time())
        conn.execute('DELETE FROM revoked_kids WHERE expires_at < ?', (now - _REVOCATION_LEEWAY,))
        conn.execute('INSERT OR IGNORE INTO revoked_kids VALUES (?, ?, ?)', (kid, now, expires_at))
    db.audit('credential.revoke', bucket, prefix, f'kid={kid}')
    return True


def revoked_since(since: int, now: int) -> list[str]:
    with closing(db.connect()) as conn:
        rows = conn.execute(
            'SELECT kid FROM revoked_kids WHERE revoked_at >= ? AND expires_at >= ? ORDER BY kid',
            (since, now - _REVOCATION_LEEWAY),
        ).fetchall()
    return [kid for (kid,) in rows]
