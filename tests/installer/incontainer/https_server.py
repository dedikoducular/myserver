#!/usr/bin/env python3
"""Sınama için yerel https dosya sunucusu (yalnızca sınama kapsayıcısında).

Kullanım: https_server.py <kök dizin> <https portu> <http portu> <sertifika> <anahtar>

  /redir/<yol>       -> 302 https://localhost:<https portu>/<yol>   (https'e yönlendirme)
  /redir-http/<yol>  -> 302 http://localhost:<http portu>/<yol>     (düz http'ye yönlendirme)
  diğer her şey      -> kök dizinden dosya

Aynı dosyalar düz http portundan da sunulur; böylece "https olmayan adrese
yönlendirme reddedilmeli" sınamasında yönlendirme izlenirse dosya gerçekten
indirilebilir olur (yani ret, sunucunun değil istemcinin kararıdır).
"""
import functools
import http.server
import ssl
import sys
import threading

root, https_port, http_port, cert, key = sys.argv[1], int(sys.argv[2]), int(sys.argv[3]), sys.argv[4], sys.argv[5]


class Handler(http.server.SimpleHTTPRequestHandler):
    def do_GET(self):
        for prefix, target in (("/redir/", "https://localhost:%d/" % https_port),
                               ("/redir-http/", "http://localhost:%d/" % http_port)):
            if self.path.startswith(prefix):
                self.send_response(302)
                self.send_header("Location", target + self.path[len(prefix):])
                self.send_header("Content-Length", "0")
                self.end_headers()
                return
        super().do_GET()

    def log_message(self, fmt, *args):
        sys.stderr.write("%s %s\n" % (self.server.server_address[1], fmt % args))


handler = functools.partial(Handler, directory=root)
plain = http.server.ThreadingHTTPServer(("127.0.0.1", http_port), handler)
threading.Thread(target=plain.serve_forever, daemon=True).start()

secure = http.server.ThreadingHTTPServer(("127.0.0.1", https_port), handler)
ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
ctx.load_cert_chain(cert, key)
secure.socket = ctx.wrap_socket(secure.socket, server_side=True)
secure.serve_forever()
