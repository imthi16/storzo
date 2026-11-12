"""Runtime settings, read from the environment once at import time."""
import os
import re
from pathlib import Path

APP_NAME = 'Storzo'
S3_ENDPOINT = os.getenv('S3_ENDPOINT', 'http://127.0.0.1:8333')
S3_PUBLIC_ENDPOINT = os.getenv('S3_PUBLIC_ENDPOINT', 'http://127.0.0.1:8333')
S3_ACCESS_KEY = os.getenv('S3_ACCESS_KEY', '')
S3_SECRET_KEY = os.getenv('S3_SECRET_KEY', '')
ADMIN_USER = os.getenv('ADMIN_USER', 'admin')
ADMIN_PASSWORD = os.getenv('ADMIN_PASSWORD', '')
SESSION_SECRET = os.getenv('SESSION_SECRET', '')
CRED_SIGNING_KEY = os.getenv('CRED_SIGNING_KEY', '')
COOKIE_SECURE = os.getenv('COOKIE_SECURE', 'false').lower() == 'true'
DB_PATH = Path(os.getenv('AUDIT_DB_PATH', '/tmp/storzo-audit.db'))
MAX_UPLOAD_MB = min(500, max(1, int(os.getenv('MAX_UPLOAD_MB', '50'))))
THUMB_MAX_MB = 12
SESSION_TTL = 8 * 60 * 60
DASHBOARD_SCAN_LIMIT = 3000
BUCKET_RE = re.compile(r'^(?!\d+\.\d+\.\d+\.\d+$)[a-z0-9](?:[a-z0-9.-]{1,61})[a-z0-9]$')
