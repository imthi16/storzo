"""Storzo: self-hosted S3 object/image management API.

This is a management plane over SeaweedFS, not a custom production S3 engine.
The S3 data plane is reached directly by S3 SDKs.
"""
from fastapi import FastAPI

from app import config
from app.db import init_db
from app.routers import audit, auth, buckets, credentials, dashboard, health, objects
from app.security import signed_serializer

app = FastAPI(title=config.APP_NAME + ' Management API', version='0.1.0', docs_url=None, redoc_url=None)
for module in (health, auth, buckets, objects, audit, dashboard, credentials):
    app.include_router(module.router)


@app.on_event('startup')
def startup():
    # Fail closed: no default admin credentials in a deployed container.
    if not config.ADMIN_PASSWORD or len(config.ADMIN_PASSWORD) < 16:
        raise RuntimeError('ADMIN_PASSWORD must have at least 16 characters')
    signed_serializer()
    if not config.S3_ACCESS_KEY or not config.S3_SECRET_KEY:
        raise RuntimeError('S3 credentials must be set')
    init_db()
