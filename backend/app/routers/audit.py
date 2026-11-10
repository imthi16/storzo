from fastapi import APIRouter, Depends, HTTPException, Query

from app import db
from app.security import require_login

router = APIRouter(prefix='/api/audit', tags=['audit'])


@router.get('')
def audit_logs(limit: int = Query(50, ge=1, le=200), offset: int = Query(0, ge=0),
               _sess: dict = Depends(require_login)):
    items, total = db.list_audit(limit, offset)
    return {'items': items, 'total': total, 'limit': limit, 'offset': offset}


@router.get('/{audit_id}')
def audit_entry(audit_id: int, _sess: dict = Depends(require_login)):
    row = db.get_audit(audit_id)
    if row is None:
        raise HTTPException(404, 'Activity entry not found')
    return row
