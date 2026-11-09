"""TCP-only disposable test peer for smoke_lite_managed_proxy_rpc.

Run on a private Docker test network, never publish these ports. Two containers
give real distinct source IPs; this does not emulate ClawManager authentication.
"""
import selectors
import socket
import socketserver
import threading


class Relay(socketserver.BaseRequestHandler):
    def handle(self):
        with socket.create_connection(("proxy-startup-runtime", self.server.server_address[1]), timeout=5) as upstream:
            upstream.settimeout(None)
            with selectors.DefaultSelector() as selector:
                selector.register(self.request, selectors.EVENT_READ, upstream)
                selector.register(upstream, selectors.EVENT_READ, self.request)
                while True:
                    for key, _ in selector.select(30):
                        data = key.fileobj.recv(65536)
                        if not data:
                            return
                        key.data.sendall(data)


class Server(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True


for port in (20017, 20018):
    server = Server(("0.0.0.0", port), Relay)
    threading.Thread(target=server.serve_forever, daemon=True).start()
threading.Event().wait()
