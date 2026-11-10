import hmac
import secrets

from fastapi import APIRouter, Depends, HTTPException, Response

from app import config
from app.db import audit
from app.schemas import Login
from app.security import require_login, require_write, signed_serializer

router = APIRouter(prefix='/api/auth', tags=['auth'])


@router.post('/login')
def login(body: Login, response: Response):
    if not (hmac.compare_digest(body.username.encode('utf-8'), config.ADMIN_USER.encode('utf-8')) and
            hmac.compare_digest(body.password.encode('utf-8'), config.ADMIN_PASSWORD.encode('utf-8'))):
        # Fixed-delay protection is better enforced by a gateway rate limit.
        raise HTTPException(401, 'Invalid credentials')
    csrf = secrets.token_urlsafe(24)
    token = signed_serializer().dumps({'user': config.ADMIN_USER, 'csrf': csrf})
    response.set_cookie('storzo_session', token, httponly=True, secure=config.COOKIE_SECURE,
                        samesite='strict', max_age=config.SESSION_TTL, path='/api')
    audit('login')
    return {'username': config.ADMIN_USER, 'csrf': csrf}


@router.get('/me')
def me(sess: dict = Depends(require_login)):
    return {'username': config.ADMIN_USER, 'csrf': sess['csrf']}


@router.post('/logout')
def logout(response: Response, _sess: dict = Depends(require_write)):
    response.delete_cookie('storzo_session', path='/api', secure=config.COOKIE_SECURE, samesite='strict')
    return {'ok': True}
