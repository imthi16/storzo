"""CI fixture that holds a designated S3 object response during cancellation."""

import http.client
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit

get_received = threading.Event()
release_get = threading.Event()
HOP_HEADERS = {
    'connection', 'keep-alive', 'proxy-authenticate', 'proxy-authorization',
    'te', 'trailer', 'transfer-encoding', 'upgrade',
}


class SourceProxy(BaseHTTPRequestHandler):
    def log_message(self, _format, *_args):
        # Request URLs and headers can carry authentication material.
        pass

    def do_POST(self):
        if self.path == '/fixture/release':
            release_get.set()
            self.send_response(204)
            self.end_headers()
        else:
            self.send_error(404)

    def do_GET(self):
        if self.path == '/fixture/received':
            self.send_response(204 if get_received.is_set() else 404)
            self.end_headers()
            return
        self.forward()

    def do_HEAD(self):
        self.forward()

    def forward(self):
        upstream = http.client.HTTPConnection('migration-source', 8333, timeout=15)
        try:
            headers = {key: value for key, value in self.headers.items()
                       if key.lower() not in HOP_HEADERS}
            upstream.request(self.command, self.path, headers=headers)
            with upstream.getresponse() as response:
                self.send_response(response.status)
                for key, value in response.getheaders():
                    if key.lower() not in HOP_HEADERS:
                        self.send_header(key, value)
                self.end_headers()
                if self.command == 'HEAD':
                    return
                if (response.status == 200
                        and urlsplit(self.path).path.endswith('/cancel/held.bin')):
                    self.wfile.flush()
                    get_received.set()
                    if not release_get.wait(timeout=90):
                        return
                while chunk := response.read(65536):
                    self.wfile.write(chunk)
        except (BrokenPipeError, ConnectionResetError):
            # The cancelled rclone process closes its connection.
            pass
        finally:
            upstream.close()


if __name__ == '__main__':
    with ThreadingHTTPServer(('0.0.0.0', 8080), SourceProxy) as server:
        server.serve_forever()
