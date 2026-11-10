"""boto3 clients for the SeaweedFS S3 endpoint."""
from functools import lru_cache

import boto3
from botocore.config import Config

from app import config


@lru_cache(maxsize=2)
def s3_client(public: bool = False):
    """Internal endpoint for server-side calls; public endpoint only to sign browser URLs."""
    if not config.S3_ACCESS_KEY or not config.S3_SECRET_KEY:
        raise RuntimeError('S3 credentials not configured')
    return boto3.client(
        's3', endpoint_url=config.S3_PUBLIC_ENDPOINT if public else config.S3_ENDPOINT,
        aws_access_key_id=config.S3_ACCESS_KEY, aws_secret_access_key=config.S3_SECRET_KEY,
        region_name='us-east-1',
        config=Config(signature_version='s3v4', s3={'addressing_style': 'path'},
                      retries={'max_attempts': 3, 'mode': 'standard'}),
    )
