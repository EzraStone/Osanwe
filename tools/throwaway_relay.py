#!/usr/bin/env python3
"""
Throwaway single-hop CONNECT relay for Osanwë Phase 0.

This exists ONLY to produce the latency measurement that gates the project
(design document §9). It is a measurement instrument, not a prototype of
`ranger`, and none of this code should survive into Phase 2.

What it does: speaks HTTP CONNECT, opens a TCP tunnel to an allowlisted
destination, and pumps bytes in both directions. The client's TLS session
runs end to end through the tunnel, so this relay cannot read the traffic it
carries — which is the property Phase 2 must preserve, and the one worth
confirming with a packet capture while you have it running.

What it deliberately is NOT:
  * not authenticated beyond a single shared secret
  * not rate limited
  * not audited
  * not a general-purpose proxy — destinations are default-deny

SAFETY. An open CONNECT proxy on a public IP is discovered by scanners within
hours and becomes someone else's spam or abuse relay. Two guards are therefore
mandatory rather than optional:

  1. --allow restricts destinations to an explicit host allowlist.
  2. --secret requires Proxy-Authorization on every CONNECT.

Run it, take the measurement, then shut it down. Do not leave it running.

Usage, on the remote VPS:

    python3 tools/throwaway_relay.py \\
        --port 8080 \\
        --secret "$(openssl rand -hex 24)" \\
        --allow api.anthropic.com

Then, from the client machine:

    python3 tools/phase0_latency.py --runs 30 \\
        --proxy http://relay-secret@vps.example:8080 \\
        --label "eu-west-1"

Python 3.9+. No dependencies.
"""

from __future__ import annotations

import argparse
import base64
import logging
import select
import socket
import sys
import threading

BUFSIZE = 65536
HANDSHAKE_TIMEOUT = 10.0
TUNNEL_TIMEOUT = 300.0

log = logging.getLogger("relay")


def _respond(conn: socket.socket, status: str, extra: str = "") -> None:
    try:
        conn.sendall(f"HTTP/1.1 {status}\r\n{extra}Connection: close\r\n\r\n".encode())
    except OSError:
        pass


def pump(a: socket.socket, b: socket.socket) -> int:
    """Shuttle bytes between two sockets until either closes. Returns bytes moved."""
    moved = 0
    socks = [a, b]
    try:
        while True:
            readable, _, errored = select.select(socks, [], socks, TUNNEL_TIMEOUT)
            if errored or not readable:
                break
            for src in readable:
                dst = b if src is a else a
                try:
                    chunk = src.recv(BUFSIZE)
                except OSError:
                    return moved
                if not chunk:
                    return moved
                try:
                    dst.sendall(chunk)
                except OSError:
                    return moved
                moved += len(chunk)
    finally:
        for s in socks:
            try:
                s.close()
            except OSError:
                pass
    return moved


def handle(conn: socket.socket, peer: tuple, allow: set[str], secret: str | None) -> None:
    conn.settimeout(HANDSHAKE_TIMEOUT)
    try:
        # Read the request head. A CONNECT head is tiny; cap it so a hostile
        # client cannot make us buffer indefinitely.
        head = b""
        while b"\r\n\r\n" not in head:
            chunk = conn.recv(BUFSIZE)
            if not chunk:
                return
            head += chunk
            if len(head) > 16384:
                _respond(conn, "431 Request Header Fields Too Large")
                return

        lines = head.split(b"\r\n")
        parts = lines[0].decode("latin-1").split()
        if len(parts) < 2 or parts[0].upper() != "CONNECT":
            _respond(conn, "405 Method Not Allowed")
            log.warning("%s: non-CONNECT request rejected", peer[0])
            return

        if secret is not None:
            expected = "Basic " + base64.b64encode(f"relay:{secret}".encode()).decode()
            supplied = next(
                (l.decode("latin-1").split(":", 1)[1].strip()
                 for l in lines[1:]
                 if l.lower().startswith(b"proxy-authorization:")),
                None,
            )
            if supplied != expected:
                _respond(conn, "407 Proxy Authentication Required",
                         'Proxy-Authenticate: Basic realm="osanwe-phase0"\r\n')
                log.warning("%s: rejected, bad or missing credentials", peer[0])
                return

        target = parts[1]
        host, _, port_s = target.rpartition(":")
        if not host:
            _respond(conn, "400 Bad Request")
            return
        try:
            port = int(port_s)
        except ValueError:
            _respond(conn, "400 Bad Request")
            return

        if (host, port) not in allow:
            _respond(conn, "403 Forbidden")
            log.warning("%s: destination %s:%d not in allowlist", peer[0], host, port)
            return

        try:
            upstream = socket.create_connection((host, port), timeout=HANDSHAKE_TIMEOUT)
        except OSError as exc:
            _respond(conn, "502 Bad Gateway")
            log.warning("%s: upstream %s:%d failed: %s", peer[0], host, port, exc)
            return

        conn.sendall(b"HTTP/1.1 200 Connection Established\r\n\r\n")
        conn.settimeout(None)
        upstream.settimeout(None)
        log.info("%s -> %s:%d tunnel open", peer[0], host, port)
        moved = pump(conn, upstream)
        log.info("%s -> %s:%d closed, %d bytes (opaque to this relay)", peer[0], host, port, moved)

    except (OSError, UnicodeDecodeError) as exc:
        log.debug("%s: %s", peer[0], exc)
    finally:
        try:
            conn.close()
        except OSError:
            pass


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--port", type=int, default=8080)
    ap.add_argument("--bind", default="0.0.0.0", help="bind address (default: all interfaces)")
    ap.add_argument("--allow", action="append", required=True, metavar="HOST[:PORT]",
                    help="allowlisted destination, port defaults to 443; repeatable. "
                         "Required — destinations are default-deny")
    ap.add_argument("--secret", help="shared secret for Proxy-Authorization. Strongly recommended")
    ap.add_argument("--insecure-no-auth", action="store_true",
                    help="run with no authentication. Only ever on a private network")
    ap.add_argument("-v", "--verbose", action="store_true")
    args = ap.parse_args()

    logging.basicConfig(
        level=logging.DEBUG if args.verbose else logging.INFO,
        format="%(asctime)s %(levelname)-7s %(message)s",
    )

    if not args.secret and not args.insecure_no_auth:
        print("error: refusing to start an unauthenticated proxy.\n"
              "       pass --secret \"$(openssl rand -hex 24)\", or --insecure-no-auth\n"
              "       if and only if this port is unreachable from the internet.",
              file=sys.stderr)
        return 1
    if args.insecure_no_auth and args.bind == "0.0.0.0":
        print("error: --insecure-no-auth with --bind 0.0.0.0 would expose an open proxy.\n"
              "       bind to a private address, or use --secret.", file=sys.stderr)
        return 1

    allow: set[tuple[str, int]] = set()
    for entry in args.allow:
        h, _, p = entry.rpartition(":")
        if h and p.isdigit():
            allow.add((h, int(p)))
        else:
            allow.add((entry, 443))

    srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind((args.bind, args.port))
    srv.listen(64)

    log.info("Phase 0 relay listening on %s:%d", args.bind, args.port)
    log.info("allowlist: %s", ", ".join(f"{h}:{p}" for h, p in sorted(allow)))
    log.info("auth: %s", "shared secret" if args.secret else "NONE (private network only)")
    log.info("this is a measurement instrument — shut it down when finished")

    try:
        while True:
            conn, peer = srv.accept()
            threading.Thread(target=handle, args=(conn, peer, allow, args.secret),
                             daemon=True).start()
    except KeyboardInterrupt:
        log.info("shutting down")
    finally:
        srv.close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
