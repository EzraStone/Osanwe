from __future__ import annotations

import base64
import socket
import threading
import unittest

import providers
import phase0_latency
import throwaway_relay


def recv_head(sock: socket.socket) -> bytes:
    data = b""
    while b"\r\n\r\n" not in data:
        chunk = sock.recv(4096)
        if not chunk:
            break
        data += chunk
    return data


class OneShotServer:
    def __init__(self, handler):
        self.sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        self.sock.bind(("127.0.0.1", 0))
        self.sock.listen(1)
        self.port = self.sock.getsockname()[1]
        self.thread = threading.Thread(target=self._serve, args=(handler,), daemon=True)

    def _serve(self, handler):
        try:
            conn, peer = self.sock.accept()
            handler(conn, peer)
        finally:
            self.sock.close()

    def start(self):
        self.thread.start()

    def join(self):
        self.thread.join(timeout=3)
        if self.thread.is_alive():
            raise AssertionError("test server did not stop")


class Phase0ProviderTests(unittest.TestCase):
    def test_all_recorded_streams_reassemble_text(self):
        for fmt, cases in providers.FIXTURES.items():
            with self.subTest(fmt=fmt):
                probe = providers.Provider("probe", fmt, "http://x", "m", None)
                text = "".join(
                    delta
                    for payload, _ in cases
                    if (delta := providers.extract_delta(probe, payload))
                )
                self.assertEqual("Hello", text)

    def test_gemini_preset_does_not_use_retired_2_0_model(self):
        self.assertEqual("gemini-3.1-flash-lite", providers.PROVIDERS["gemini"].model)

    def test_groq_free_tier_pacing_stays_below_30_rpm(self):
        self.assertGreaterEqual(providers.PROVIDERS["groq"].suggested_delay, 2.0)

    def test_report_is_encodable_by_windows_legacy_console(self):
        summary = {
            "n": 5,
            "failed": 0,
            "ttft_p50": 300.0,
            "ttft_p95": 350.0,
            "ttft_p99": 350.0,
            "ttft_mean": 310.0,
            "total_p50": 400.0,
            "intertoken_p50": 1.0,
            "intertoken_p95": 2.0,
        }
        faster_proxy = dict(summary, ttft_p50=250.0)
        meta = {
            "label": "console test",
            "provider": "groq",
            "model": "test-model",
            "runs": 5,
            "max_tokens": 16,
            "warm": True,
            "timestamp": "2026-08-10 00:00:00 CDT",
        }

        report = phase0_latency.render_markdown(summary, faster_proxy, meta)

        report.encode("cp1252")
        self.assertIn("-50 ms", report)


class Phase0RelayTests(unittest.TestCase):
    secret = "phase0-test-secret"

    def run_relay_once(self, allow):
        relay = OneShotServer(
            lambda conn, peer: throwaway_relay.handle(conn, peer, allow, self.secret)
        )
        relay.start()
        return relay

    def connect(self, relay_port, target, *, authorized):
        client = socket.create_connection(("127.0.0.1", relay_port), timeout=2)
        headers = [f"CONNECT {target} HTTP/1.1", f"Host: {target}"]
        if authorized:
            token = base64.b64encode(f"relay:{self.secret}".encode()).decode()
            headers.append(f"Proxy-Authorization: Basic {token}")
        client.sendall(("\r\n".join(headers) + "\r\n\r\n").encode())
        return client

    def test_missing_authentication_is_rejected(self):
        relay = self.run_relay_once({("127.0.0.1", 443)})
        client = self.connect(relay.port, "127.0.0.1:443", authorized=False)
        self.assertIn(b"407 Proxy Authentication Required", recv_head(client))
        client.close()
        relay.join()

    def test_destination_outside_allowlist_is_rejected(self):
        relay = self.run_relay_once({("127.0.0.1", 443)})
        client = self.connect(relay.port, "127.0.0.1:444", authorized=True)
        self.assertIn(b"403 Forbidden", recv_head(client))
        client.close()
        relay.join()

    def test_authenticated_allowlisted_tunnel_moves_opaque_bytes(self):
        upstream = OneShotServer(self.echo)
        upstream.start()
        relay = self.run_relay_once({("127.0.0.1", upstream.port)})
        client = self.connect(relay.port, f"127.0.0.1:{upstream.port}", authorized=True)
        self.assertIn(b"200 Connection Established", recv_head(client))
        payload = b"opaque-phase0-test"
        client.sendall(payload)
        self.assertEqual(payload, client.recv(len(payload)))
        client.close()
        relay.join()
        upstream.join()

    @staticmethod
    def echo(conn, _peer):
        try:
            data = conn.recv(4096)
            if data:
                conn.sendall(data)
        finally:
            conn.close()


if __name__ == "__main__":
    unittest.main()
