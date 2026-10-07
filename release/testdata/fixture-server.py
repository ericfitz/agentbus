#!/usr/bin/env python3
"""Serve a fake GitHub releases tree for release/test-install.sh.

usage: fixture-server.py <root> <port>

<root>/<variant>/latest          one line: the tag `releases/latest` redirects to
<root>/<variant>/<tag>/<asset>   release assets

GET|HEAD /<variant>/releases/latest                  -> 302 /<variant>/releases/tag/<tag>
GET|HEAD /<variant>/releases/download/<tag>/<asset>  -> the file
anything else -> 404; a path part of ".." -> 400
"""

import http.server
import os
import sys

ROOT = os.path.abspath(sys.argv[1])
PORT = int(sys.argv[2])


class Handler(http.server.BaseHTTPRequestHandler):
    def route(self):
        parts = self.path.split("?", 1)[0].strip("/").split("/")
        if any(p in ("", ".", "..") for p in parts):
            return self.status(400)
        if len(parts) == 3 and parts[1:] == ["releases", "latest"]:
            latest = os.path.join(ROOT, parts[0], "latest")
            if not os.path.isfile(latest):
                return self.status(404)
            with open(latest, encoding="ascii") as f:
                tag = f.read().strip()
            self.send_response(302)
            self.send_header("Location", f"/{parts[0]}/releases/tag/{tag}")
            self.end_headers()
            return None
        if len(parts) == 5 and parts[1:3] == ["releases", "download"]:
            path = os.path.join(ROOT, parts[0], parts[3], parts[4])
            if not os.path.isfile(path):
                return self.status(404)
            with open(path, "rb") as f:
                data = f.read()
            self.send_response(200)
            self.send_header("Content-Type", "application/octet-stream")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            if self.command == "GET":
                self.wfile.write(data)
            return None
        return self.status(404)

    def status(self, code):
        self.send_response(code)
        self.send_header("Content-Length", "0")
        self.end_headers()

    do_GET = route
    do_HEAD = route

    def log_message(self, *args):  # quiet
        pass


http.server.ThreadingHTTPServer(("0.0.0.0", PORT), Handler).serve_forever()
