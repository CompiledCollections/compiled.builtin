# GNU General Public License v2.0+ (see https://www.gnu.org/licenses/old-licenses/gpl-2.0.txt)
"""HTTP test server for the uri integration tests. Standard library only.

Serves the same endpoints over plain HTTP, HTTPS, HTTPS requiring a client
certificate, and a unix socket. Plain HTTP also works as an HTTP proxy:
requests with an absolute URL are answered as if forwarded, with
"proxied": true in /echo responses.

    python3 testserver.py --dir DIR

DIR must hold ca.pem, server.pem and server.key. The chosen ports and socket
path are written to DIR/ports.json; the process id to DIR/server.pid.

Endpoints (on every listener):
    /echo                     request method, path, query, headers and body as JSON
    /status/CODE              respond with CODE (?reason= sets the reason phrase)
    /redirect/CODE?to=PATH    redirect with CODE to PATH (default /echo)
    /redirect-loop            redirect to itself
    /basic-auth/USER/PASS     HTTP Basic auth
    /digest-auth/QOP/USER/PASS[/ALGORITHM]   HTTP Digest auth (QOP: auth or none)
    /cookies/set?k=v          set cookies, then echo
    /cookies/redirect?k=v     set cookies and redirect to /echo
    /gzip                     gzip-encoded JSON
    /delay/SECONDS            wait, then echo
    /dup-headers              repeated headers, including two Set-Cookie
    /content?ct=TYPE&body=B   respond B with Content-Type TYPE (?b64= for raw bytes)
    /download/NAME[?cd=FILE]  a file body; 304 when If-Modified-Since is sent
    /no-length                body without Content-Length
    /location?to=PATH         201 with a Location header
"""

import argparse
import base64
import email.parser
import email.policy
import gzip
import hashlib
import http.server
import json
import os
import socket
import socketserver
import ssl
import threading
import time
import urllib.parse
import urllib.request


def parse_multipart(content_type, body):
    """Parts of a multipart body as dicts, to show what a client sent."""
    msg = email.parser.BytesParser(policy=email.policy.HTTP).parsebytes(
        b'Content-Type: ' + content_type.encode() + b'\r\n\r\n' + body)
    parts = []
    for part in msg.iter_parts():
        payload = part.get_payload(decode=True) or b''
        parts.append({
            'name': part.get_param('name', header='content-disposition'),
            'filename': part.get_param('filename', header='content-disposition'),
            'content_type': part.get_content_type(),
            'transfer_encoding': part.get('Content-Transfer-Encoding'),
            'content': payload.decode('utf-8', 'replace'),
        })
    return parts


class Handler(http.server.BaseHTTPRequestHandler):
    def version_string(self):
        return 'compiled-builtin-test'

    def log_message(self, *args):
        pass

    def address_string(self):
        return str(self.client_address[0]) if self.client_address else 'unix'

    def _reply(self, code, body=b'', content_type='application/json', headers=(), reason=None, length=True):
        self.send_response(code, reason)
        if body or code not in (204, 304):
            self.send_header('Content-Type', content_type)
        for name, value in headers:
            self.send_header(name, value)
        if length and code not in (204, 304):
            self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        if self.command != 'HEAD' and code not in (204, 304):
            self.wfile.write(body)

    def _body(self):
        length = int(self.headers.get('Content-Length') or 0)
        return self.rfile.read(length) if length else b''

    def _echo(self, extra=None, headers=()):
        body = self._body()
        names = {}
        for name, value in self.headers.items():
            name = name.lower()
            names[name] = names[name] + ', ' + value if name in names else value
        # Digest responses change on every request; only say one was sent.
        if names.get('authorization', '').startswith('Digest '):
            names['authorization'] = 'Digest (sent)'
        data = {
            'method': self.command,
            'path': self.url.path,
            'query': self.url.query,
            'headers': names,
            'body': body.decode('utf-8', 'replace'),
        }
        content_type = self.headers.get('Content-Type', '')
        if content_type.startswith('multipart/'):
            data['multipart'] = parse_multipart(content_type, body)
        if self.proxied:
            data['proxied'] = True
        data.update(extra or {})
        self._reply(200, json.dumps(data, sort_keys=True).encode(), headers=headers)

    def _handle(self):
        target = self.path
        self.proxied = False
        if target.startswith('http://'):  # proxy request with an absolute URL
            self.proxied = True
            target = urllib.parse.urlsplit(target)._replace(scheme='', netloc='').geturl() or '/'
        self.url = urllib.parse.urlsplit(target)
        query = dict(urllib.parse.parse_qsl(self.url.query, keep_blank_values=True))
        parts = [p for p in self.url.path.split('/') if p]
        route = parts[0] if parts else ''

        if route == 'echo':
            return self._echo()
        if route == 'status':
            code = int(parts[1])
            self._body()
            return self._reply(code, b'' if code in (204, 304) else ('status %d' % code).encode(),
                               'text/plain', reason=query.get('reason'))
        if route == 'redirect':
            self._body()
            return self._reply(int(parts[1]), b'', 'text/plain', headers=[('Location', query.get('to', '/echo'))])
        if route == 'redirect-loop':
            return self._reply(302, b'', 'text/plain', headers=[('Location', '/redirect-loop')])
        if route == 'basic-auth':
            user, password = parts[1], parts[2]
            expected = 'Basic ' + base64.b64encode(('%s:%s' % (user, password)).encode()).decode()
            if self.headers.get('Authorization') != expected:
                self._body()
                return self._reply(401, b'unauthorized', 'text/plain',
                                   headers=[('WWW-Authenticate', 'Basic realm="test"')])
            return self._echo({'authenticated': True, 'user': user})
        if route == 'digest-auth':
            return self._digest(parts[1], parts[2], parts[3], parts[4] if len(parts) > 4 else 'MD5')
        if route == 'cookies':
            cookies = [('Set-Cookie', '%s=%s; Path=/' % kv) for kv in query.items()]
            if len(parts) > 1 and parts[1] == 'redirect':
                return self._reply(302, b'', 'text/plain', headers=cookies + [('Location', '/echo')])
            return self._echo(headers=cookies)
        if route == 'gzip':
            body = gzip.compress(json.dumps({'gzipped': True}).encode())
            return self._reply(200, body, headers=[('Content-Encoding', 'gzip')])
        if route == 'delay':
            time.sleep(float(parts[1]))
            return self._echo()
        if route == 'dup-headers':
            return self._reply(200, b'{}', headers=[
                ('X-Dup', 'one'), ('X-Dup', 'two'),
                ('Set-Cookie', 'first=1; Path=/'), ('Set-Cookie', 'second=2; Path=/'),
            ])
        if route == 'content':
            body = base64.b64decode(query['b64']) if 'b64' in query else query.get('body', '').encode()
            return self._reply(200, body, query.get('ct', 'text/plain'))
        if route == 'download':
            if self.headers.get('If-Modified-Since'):
                return self._reply(304)
            headers = []
            if 'cd' in query:
                headers.append(('Content-Disposition', 'attachment; filename="%s"' % query['cd']))
            return self._reply(200, ('downloaded %s\n' % parts[1]).encode(), 'application/octet-stream', headers)
        if route == 'no-length':
            self.send_response(200)
            self.send_header('Content-Type', 'text/plain')
            self.end_headers()
            self.wfile.write(b'no length\n')
            self.close_connection = True
            return None
        if route == 'location':
            return self._reply(201, b'created', 'text/plain', headers=[('Location', query.get('to', 'new'))])
        return self._reply(404, b'not found', 'text/plain')

    def _digest(self, qop, user, password, algorithm):
        hashes = {'MD5': hashlib.md5, 'SHA': hashlib.sha1, 'SHA-256': hashlib.sha256}

        def H(s):
            return hashes[algorithm](s.encode()).hexdigest()

        auth = self.headers.get('Authorization', '')
        if auth.startswith('Digest '):
            fields = dict(urllib.request.parse_keqv_list(urllib.request.parse_http_list(auth[7:])))
            ha1 = H('%s:test:%s' % (user, password))
            ha2 = H('%s:%s' % (self.command, fields.get('uri', '')))
            if qop == 'auth':
                expected = H(':'.join([ha1, fields.get('nonce', ''), fields.get('nc', ''),
                                       fields.get('cnonce', ''), 'auth', ha2]))
            else:
                expected = H('%s:%s:%s' % (ha1, fields.get('nonce', ''), ha2))
            if fields.get('username') == user and fields.get('response') == expected:
                return self._echo({'authenticated': True, 'user': user})
        self._body()
        challenge = 'Digest realm="test", nonce="%s", algorithm=%s' % (os.urandom(8).hex(), algorithm)
        if qop == 'auth':
            challenge += ', qop="auth"'
        return self._reply(401, b'unauthorized', 'text/plain', headers=[('WWW-Authenticate', challenge)])

    def __getattr__(self, name):
        if name.startswith('do_'):
            return self._handle
        raise AttributeError(name)


class ThreadingHTTPServer(socketserver.ThreadingMixIn, http.server.HTTPServer):
    daemon_threads = True


class ThreadingUnixServer(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
    daemon_threads = True


class TLSServer(ThreadingHTTPServer):
    """Does the TLS handshake in the request's thread, and closes a failed one
    gracefully.

    After rejecting a handshake (no client certificate, say) the server has
    sent its alert, but with TLS 1.3 the client has already sent more: its
    Finished message, often the request. Closing a socket with unread data
    makes the kernel send a reset, which can reach the client before the
    alert, so it reports "connection reset by peer" instead of the alert.
    So stop writing, read until the client closes, then close.
    """

    def finish_request(self, request, client_address):
        request.settimeout(10)
        try:
            request.do_handshake()
        except (ssl.SSLError, OSError):
            linger_close(request)
            return
        request.settimeout(None)
        super().finish_request(request, client_address)


def linger_close(sock):
    try:
        sock.shutdown(socket.SHUT_WR)  # also drops the TLS layer: recv reads raw bytes
        while sock.recv(65536):
            pass
    except OSError:
        pass
    sock.close()


def tls_server(directory, client_certs):
    server = TLSServer(('127.0.0.1', 0), Handler)
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain(os.path.join(directory, 'server.pem'), os.path.join(directory, 'server.key'))
    if client_certs:
        context.verify_mode = ssl.CERT_REQUIRED
        context.load_verify_locations(os.path.join(directory, 'ca.pem'))
    server.socket = context.wrap_socket(server.socket, server_side=True, do_handshake_on_connect=False)
    return server


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--dir', required=True)
    args = parser.parse_args()

    socket_path = os.path.join(args.dir, 'http.sock')
    if os.path.exists(socket_path):
        os.remove(socket_path)

    servers = {
        'http': ThreadingHTTPServer(('127.0.0.1', 0), Handler),
        'https': tls_server(args.dir, client_certs=False),
        'mtls': tls_server(args.dir, client_certs=True),
        'unix': ThreadingUnixServer(socket_path, Handler),
    }
    for server in servers.values():
        threading.Thread(target=server.serve_forever, daemon=True).start()

    # A port nothing listens on, for connection failures.
    closed = socket.socket()
    closed.bind(('127.0.0.1', 0))
    closed_port = closed.getsockname()[1]
    closed.close()

    info = {name: s.server_address[1] for name, s in servers.items() if name != 'unix'}
    info.update(unix=socket_path, closed=closed_port)
    with open(os.path.join(args.dir, 'ports.json.tmp'), 'w') as f:
        json.dump(info, f)
    os.rename(os.path.join(args.dir, 'ports.json.tmp'), os.path.join(args.dir, 'ports.json'))
    with open(os.path.join(args.dir, 'server.pid'), 'w') as f:
        f.write(str(os.getpid()))

    while True:
        time.sleep(3600)


if __name__ == '__main__':
    main()
