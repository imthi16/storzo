from pydantic import BaseModel


class Login(BaseModel):
    username: str
    password: str


class BucketCreate(BaseModel):
    name: str


class MigrationSource(BaseModel):
    endpoint: str | None = None
    region: str = 'us-east-1'
    bucket: str
    prefix: str = ''
    access_key_id: str
    secret_access_key: str


class MigrationDestination(BaseModel):
    bucket: str
    prefix: str = ''


class MigrationRequest(BaseModel):
    source: MigrationSource
    destination: MigrationDestination
