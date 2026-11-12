"""Storage statistics for the dashboard (bounded scan, no extra S3 calls beyond listing)."""
from app import config, s3

IMAGE_EXTENSIONS = {'jpg', 'jpeg', 'png', 'webp', 'gif', 'avif'}
TYPE_EXTENSIONS = (
    ('image', IMAGE_EXTENSIONS),
    ('video', {'mp4', 'webm', 'mov', 'avi', 'mkv', 'm4v', 'mpeg', 'mpg'}),
    ('audio', {'mp3', 'wav', 'ogg', 'flac', 'aac', 'm4a', 'opus', 'aiff'}),
    ('document', {'pdf', 'txt', 'md', 'csv', 'json', 'xml', 'doc', 'docx', 'xls', 'xlsx', 'ppt', 'pptx', 'odt', 'rtf'}),
    ('archive', {'zip', 'tar', 'gz', 'bz2', 'xz', '7z', 'rar', 'tgz'}),
)


def object_type(key: str) -> str:
    name = key.rsplit('/', 1)[-1].lower()
    extension = name.rsplit('.', 1)[-1] if '.' in name else ''
    for kind, extensions in TYPE_EXTENSIONS:
        if extension in extensions:
            return kind
    return 'other'


def collect_dashboard() -> dict:
    limit = config.DASHBOARD_SCAN_LIMIT
    buckets_ = s3.s3_client().list_buckets().get('Buckets', [])
    total, bytes_, images, scanned, capped = 0, 0, 0, 0, False
    bucket_rows = [{'name': bucket['Name'], 'object_count': 0, 'total_bytes': 0} for bucket in buckets_]
    types: dict[str, dict] = {}
    largest: list[dict] = []
    recent: list[tuple[float, dict]] = []
    for bucket, summary in zip(buckets_, bucket_rows, strict=True):
        token = None
        while scanned < limit:
            args = {'Bucket': bucket['Name'], 'MaxKeys': min(500, limit - scanned)}
            if token:
                args['ContinuationToken'] = token
            res = s3.s3_client().list_objects_v2(**args)
            for obj in res.get('Contents', []):
                size = obj.get('Size', 0)
                total += 1
                bytes_ += size
                images += int(obj['Key'].lower().endswith(tuple('.' + e for e in IMAGE_EXTENSIONS)))
                summary['object_count'] += 1
                summary['total_bytes'] += size
                kind = object_type(obj['Key'])
                group = types.setdefault(kind, {'type': kind, 'count': 0, 'bytes': 0})
                group['count'] += 1
                group['bytes'] += size
                modified = obj.get('LastModified')
                item = {'bucket': bucket['Name'], 'key': obj['Key'], 'size': size,
                        'modified': modified.isoformat() if modified else ''}
                largest.append(item)
                largest.sort(key=lambda row: row['size'], reverse=True)
                del largest[5:]
                recent.append((modified.timestamp() if modified else float('-inf'), item))
                recent.sort(key=lambda row: row[0], reverse=True)
                del recent[8:]
            scanned += len(res.get('Contents', []))
            token = res.get('NextContinuationToken')
            if not token:
                break
            if scanned >= limit:
                capped = True
        if scanned >= limit:
            capped = True
            break
    return {'bucket_count': len(buckets_), 'object_count': total, 'total_bytes': bytes_,
            'image_count': images, 'sampled': capped,
            'message': f'Statistics scan is capped at {limit} objects; not full billing metrics.' if capped else '',
            'buckets': sorted(bucket_rows, key=lambda row: row['total_bytes'], reverse=True),
            'by_type': sorted(types.values(), key=lambda row: row['bytes'], reverse=True),
            'largest': largest, 'recent': [item for _, item in recent]}
