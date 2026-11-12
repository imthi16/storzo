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
import secrets
import time
import uuid

from app import config

MAX_TTL = 3600
SKEW = 60


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
