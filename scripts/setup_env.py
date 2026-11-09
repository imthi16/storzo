#!/usr/bin/env python3
"""Generate a local .env; never overwrite existing credentials."""
from pathlib import Path
import secrets
import string

root = Path(__file__).resolve().parents[1]
p = root / '.env'
if p.exists():
    print('Existing .env preserved; no secrets changed.')
    raise SystemExit(0)
chars = string.ascii_letters + string.digits

def key(length: int):
    return ''.join(secrets.choice(chars) for _ in range(length))

data = {
    'ADMIN_USER': 'admin',
    'ADMIN_PASSWORD': key(30),
    'SESSION_SECRET': secrets.token_urlsafe(48),
    'S3_ACCESS_KEY': key(24),
    'S3_SECRET_KEY': key(48),
    'WEED_ADMIN_PASSWORD': key(30),
    'S3_PUBLIC_ENDPOINT': 'http://localhost:8333',
    'UI_BIND_IP': '127.0.0.1',
    'S3_BIND_IP': '127.0.0.1',
    'COOKIE_SECURE': 'false',
    'MAX_UPLOAD_MB': '50',
    'SEAWEEDFS_TAG': 'latest',
}
p.write_text(''.join(f'{k}={v}\n' for k,v in data.items()))
p.chmod(0o600)
print('Generated .env with randomized credentials. Keep this file secret.')
print('Admin username: admin')
print('Admin password: run `grep ADMIN_PASSWORD .env` on your own machine.')
