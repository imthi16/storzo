"""Generate gateway fixtures with the real issuer and botocore.

Run from backend: python3 tests/gateway_vectors.py
The signing key is public synthetic test data; time is fixed, IDs are random.
"""
import json
import sys
from datetime import UTC, datetime
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from botocore.auth import S3SigV4Auth, S3SigV4QueryAuth
from botocore.awsrequest import AWSRequest
from botocore.credentials import Credentials

from app import config, credentials


def main():
    config.CRED_SIGNING_KEY = 'gateway-public-test-signing-key-1234567890'
    now = 1800000000
    vectors = []
    count = 0
    for mode in ('ro', 'rw'):
        cred = credentials.issue('agent-data', 'run-42/', mode, 900, now=now)
        credentials.verify(cred['session_token'], now=now)
        requests = []
        cases = [
            ('GET', '/agent-data/run-42/a%20b%2Bc.txt', '', False),
            ('GET', '/agent-data/run-42/a%20b%2Bc.txt', '', True),
            ('GET', '/agent-data?list-type=2&prefix=run-42%2F&delimiter=%2F', '', False),
            ('GET', '/agent-data?list-type=2&prefix=run-42%2F&delimiter=%2F', '', True),
            ('PUT', '/agent-data/run-42/upload', 'hello gateway', False),
            ('PUT', '/agent-data/run-42/upload', 'hello gateway', True),
        ]
        for method, path, body, presigned in cases:
            req = AWSRequest(method=method, url='https://s3.example.test' + path, data=b'' if presigned else body.encode())
            auth = Credentials(cred['access_key_id'], cred['secret_access_key'], cred['session_token'])
            signer = S3SigV4QueryAuth(auth, 's3', 'us-east-1', expires=300) if presigned else S3SigV4Auth(auth, 's3', 'us-east-1')
            with patch('botocore.auth.get_current_datetime', return_value=datetime.fromtimestamp(now, UTC)):
                signer.add_auth(req)
            requests.append({'method': req.method, 'url': req.url, 'body': body, 'headers': dict(req.headers)})
            count += 1
        vectors.append({**cred, 'requests': requests})
    dest = Path(__file__).resolve().parents[2] / 'gateway/internal/verify/testdata/python.json'
    dest.write_text(json.dumps({'signing_key': config.CRED_SIGNING_KEY, 'now': now, 'vectors': vectors}, indent=2) + '\n')
    print(f'Wrote {len(vectors)} Python credentials and {count} botocore signed requests to {dest}')


if __name__ == '__main__':
    main()
