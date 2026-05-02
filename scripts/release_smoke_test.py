#!/usr/bin/env python3
import argparse
import hashlib
import json
import os
import socket
import subprocess
import sys
import tempfile
import textwrap
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


CLIENT_API_KEY = "client-test-key"
MANAGEMENT_SECRET = "management-test-secret"
SUCCESS_UPSTREAM_KEY = "upstream-good-key"
FAIL_UPSTREAM_KEY = "upstream-bad-key"
BAD_PROXY_URL = "http://127.0.0.1:9"


class CheckFailed(RuntimeError):
    pass


def free_port():
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def assert_true(condition, message):
    if not condition:
        raise CheckFailed(message)


def wait_until(name, fn, timeout=30, interval=0.5):
    deadline = time.time() + timeout
    last_error = None
    while time.time() < deadline:
        try:
            return fn()
        except Exception as exc:
            last_error = exc
            time.sleep(interval)
    raise CheckFailed(f"{name} timed out: {last_error}")


def request(method, url, *, headers=None, body=None, timeout=20):
    req = urllib.request.Request(url, method=method, headers=headers or {})
    data = None
    if body is not None:
        if isinstance(body, (dict, list)):
            data = json.dumps(body).encode("utf-8")
            req.add_header("Content-Type", "application/json")
        elif isinstance(body, str):
            data = body.encode("utf-8")
        else:
            data = body
    try:
        with urllib.request.urlopen(req, data=data, timeout=timeout) as resp:
            text = resp.read().decode("utf-8", errors="replace")
            return resp.status, dict(resp.headers), text
    except urllib.error.HTTPError as exc:
        text = exc.read().decode("utf-8", errors="replace")
        return exc.code, dict(exc.headers), text


def request_json(method, url, *, headers=None, body=None, timeout=20):
    status, resp_headers, text = request(method, url, headers=headers, body=body, timeout=timeout)
    parsed = None
    if text:
        try:
            parsed = json.loads(text)
        except Exception:
            parsed = text
    else:
        parsed = {}
    return status, resp_headers, text, parsed


def build_config(port, mock_port, auth_dir):
    panel_url = f"http://127.0.0.1:{mock_port}/panel/releases/latest"
    return textwrap.dedent(
        f"""
        host: "127.0.0.1"
        port: {port}
        auth-dir: "{auth_dir}"
        request-log: true
        usage-statistics-enabled: true
        logging-to-file: false
        remote-management:
          allow-remote: false
          secret-key: "{MANAGEMENT_SECRET}"
          panel-github-repository: "{panel_url}"
        api-keys:
          - "{CLIENT_API_KEY}"
        openai-compatibility:
          - name: "success-provider"
            base-url: "http://127.0.0.1:{mock_port}/success/v1"
            api-key-entries:
              - api-key: "{SUCCESS_UPSTREAM_KEY}"
                proxy-url: "direct"
            models:
              - name: "upstream-ok"
                alias: "ok-model"
          - name: "fail-provider"
            base-url: "http://127.0.0.1:{mock_port}/fail/v1"
            status-rules:
              - name: "401 unauthorized disable"
                status: 401
                body-equals: "unauthorized"
                action: "disable"
            api-key-entries:
              - api-key: "{FAIL_UPSTREAM_KEY}"
                proxy-url: "direct"
            models:
              - name: "upstream-fail"
                alias: "fail-model"
        """
    ).strip() + "\n"


def start_binary(binary_path, config_path, output_dir):
    env = os.environ.copy()
    env.pop("MANAGEMENT_PASSWORD", None)
    env.pop("MANAGEMENT_STATIC_PATH", None)

    stdout_path = Path(output_dir) / "cpa.stdout.log"
    stderr_path = Path(output_dir) / "cpa.stderr.log"
    stdout = open(stdout_path, "w", encoding="utf-8")
    stderr = open(stderr_path, "w", encoding="utf-8")
    process = subprocess.Popen(
        [str(binary_path), "-config", str(config_path)],
        stdout=stdout,
        stderr=stderr,
        env=env,
    )
    return process, stdout, stderr


def stop_process(process):
    if process is None or process.poll() is not None:
        return
    process.terminate()
    try:
        process.wait(timeout=10)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=10)


def find_log_files(*base_dirs):
    out = []
    seen = set()
    for base_dir in base_dirs:
        if not base_dir:
            continue
        root = Path(base_dir)
        if not root.exists():
            continue
        for path in root.rglob("*.log"):
            if path.name in {"cpa.stdout.log", "cpa.stderr.log"}:
                continue
            key = str(path.resolve())
            if key in seen:
                continue
            seen.add(key)
            out.append(path)
    return sorted(out)


def extract_request_id(path):
    name = path.name
    if not name.endswith(".log") or "-" not in name:
        return ""
    return name.rsplit("-", 1)[-1][:-4]


def make_mock_handler(management_html_bytes, mock_port):
    management_sha = hashlib.sha256(management_html_bytes).hexdigest()

    class MockHandler(BaseHTTPRequestHandler):
        def log_message(self, fmt, *args):
            return

        def _send_bytes(self, status, body, content_type):
            self.send_response(status)
            self.send_header("Content-Type", content_type)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def _send_json(self, status, payload):
            self._send_bytes(status, json.dumps(payload).encode("utf-8"), "application/json")

        def _send_text(self, status, payload):
            self._send_bytes(status, payload.encode("utf-8"), "text/plain; charset=utf-8")

        def do_GET(self):
            auth = self.headers.get("Authorization", "")
            if self.path == "/panel/releases/latest":
                self._send_json(
                    200,
                    {
                        "assets": [
                            {
                                "name": "management.html",
                                "browser_download_url": f"http://127.0.0.1:{mock_port}/panel/assets/management.html",
                                "digest": f"sha256:{management_sha}",
                            }
                        ]
                    },
                )
                return
            if self.path == "/panel/assets/management.html":
                self._send_bytes(200, management_html_bytes, "text/html; charset=utf-8")
                return
            if self.path == "/success/v1/ping":
                if auth != f"Bearer {SUCCESS_UPSTREAM_KEY}":
                    self._send_text(401, "bad upstream auth")
                    return
                self._send_json(200, {"ok": True})
                return
            self._send_text(404, "not found")

        def do_POST(self):
            auth = self.headers.get("Authorization", "")
            length = int(self.headers.get("Content-Length", "0") or "0")
            payload = self.rfile.read(length).decode("utf-8", errors="replace") if length > 0 else ""

            if self.path == "/success/v1/chat/completions":
                if auth != f"Bearer {SUCCESS_UPSTREAM_KEY}":
                    self._send_text(401, "bad upstream auth")
                    return
                self._send_json(
                    200,
                    {
                        "id": "chatcmpl-smoke",
                        "object": "chat.completion",
                        "choices": [
                            {
                                "index": 0,
                                "message": {"role": "assistant", "content": "ok"},
                                "finish_reason": "stop",
                            }
                        ],
                        "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
                        "echo": payload,
                    },
                )
                return

            if self.path == "/fail/v1/chat/completions":
                if auth != f"Bearer {FAIL_UPSTREAM_KEY}":
                    self._send_text(401, "unauthorized")
                    return
                self._send_text(401, "unauthorized")
                return

            self._send_text(404, "not found")

    return MockHandler


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", required=True)
    parser.add_argument("--management-html", required=True)
    parser.add_argument("--output-dir", required=True)
    args = parser.parse_args()

    output_dir = Path(args.output_dir).resolve()
    output_dir.mkdir(parents=True, exist_ok=True)

    management_html_bytes = Path(args.management_html).read_bytes()
    mock_port = free_port()
    cpa_port = free_port()
    work_dir = Path(tempfile.mkdtemp(prefix="cpa-smoke-", dir=str(output_dir)))
    auth_dir = work_dir / "auths"
    auth_dir.mkdir(parents=True, exist_ok=True)
    config_path = work_dir / "config.yaml"
    config_path.write_text(build_config(cpa_port, mock_port, auth_dir), encoding="utf-8")

    handler_cls = make_mock_handler(management_html_bytes, mock_port)
    mock_server = ThreadingHTTPServer(("127.0.0.1", mock_port), handler_cls)
    mock_thread = threading.Thread(target=mock_server.serve_forever, daemon=True)
    mock_thread.start()
    session_start = time.time()

    process = None
    stdout = None
    stderr = None
    base_url = f"http://127.0.0.1:{cpa_port}"
    mgmt_headers = {"Authorization": f"Bearer {MANAGEMENT_SECRET}"}
    client_headers = {
        "Authorization": f"Bearer {CLIENT_API_KEY}",
        "Content-Type": "application/json",
    }

    try:
        process, stdout, stderr = start_binary(args.binary, config_path, output_dir)

        def health_ready():
            if process.poll() is not None:
                raise CheckFailed(f"process exited early with code {process.returncode}")
            status, _, text = request("GET", f"{base_url}/healthz", timeout=5)
            assert_true(status == 200, f"/healthz failed: {status} {text}")
            return True

        wait_until("healthz", health_ready, timeout=40)

        status, _, text = request("GET", f"{base_url}/management.html", timeout=10)
        assert_true(status == 200, f"/management.html failed: {status}")
        assert_true("html" in text.lower(), "management.html did not return html")

        downloaded_panel = work_dir / "static" / "management.html"
        assert_true(downloaded_panel.exists(), "management.html was not downloaded to static directory")
        assert_true(downloaded_panel.read_bytes() == management_html_bytes, "downloaded management.html mismatch")

        config_after_boot = config_path.read_text(encoding="utf-8")
        assert_true(MANAGEMENT_SECRET not in config_after_boot, "secret-key plaintext was not hashed")
        assert_true("$2" in config_after_boot, "secret-key hash not found in config")

        status, _, text = request("GET", f"{base_url}/v1/models", timeout=10)
        assert_true(status in {401, 403}, f"anonymous api request should fail, got {status}: {text}")

        status, _, _, body = request_json(
            "GET",
            f"{base_url}/v0/management/openai-compatibility",
            headers=mgmt_headers,
        )
        assert_true(status == 200, f"read openai-compatibility failed: {status}")
        providers = body.get("openai-compatibility", [])
        assert_true(len(providers) == 2, f"unexpected provider count: {len(providers)}")

        success_provider = next((item for item in providers if item.get("name") == "success-provider"), None)
        fail_provider = next((item for item in providers if item.get("name") == "fail-provider"), None)
        assert_true(success_provider is not None, "missing success-provider")
        assert_true(fail_provider is not None, "missing fail-provider")
        success_auth_index = success_provider["api-key-entries"][0].get("auth-index", "")
        fail_auth_index = fail_provider["api-key-entries"][0].get("auth-index", "")
        assert_true(bool(success_auth_index), "success-provider auth-index missing")
        assert_true(bool(fail_auth_index), "fail-provider auth-index missing")

        status, _, _, body = request_json(
            "PATCH",
            f"{base_url}/v0/management/proxy-url",
            headers=mgmt_headers,
            body={"value": BAD_PROXY_URL},
        )
        assert_true(status == 200, f"set global proxy failed: {status} {body}")

        status, _, _, body = request_json(
            "POST",
            f"{base_url}/v0/management/api-call",
            headers=mgmt_headers,
            body={
                "authIndex": success_auth_index,
                "method": "GET",
                "url": f"http://127.0.0.1:{mock_port}/success/v1/ping",
                "header": {"Authorization": "Bearer $TOKEN$"},
            },
        )
        assert_true(status == 200, f"management api-call failed: {status} {body}")
        assert_true(body.get("status_code") == 200, f"management api-call upstream failed: {body}")

        status, _, text = request(
            "POST",
            f"{base_url}/v1/chat/completions",
            headers=client_headers,
            body={"model": "ok-model", "messages": [{"role": "user", "content": "hi"}], "stream": False},
        )
        assert_true(status == 200, f"ok-model request failed: {status} {text}")
        ok_body = json.loads(text)
        assert_true(ok_body["choices"][0]["message"]["content"] == "ok", "ok-model response mismatch")

        def usage_ready():
            status2, _, _, body2 = request_json("GET", f"{base_url}/v0/management/usage", headers=mgmt_headers)
            assert_true(status2 == 200, f"read usage failed: {status2}")
            usage = body2.get("usage", {})
            assert_true(int(usage.get("total_requests", 0)) >= 1, f"usage total_requests invalid: {usage}")
            return usage

        usage = wait_until("usage", usage_ready, timeout=15)
        assert_true(isinstance(usage, dict), "usage response is invalid")

        status, _, _, body = request_json("GET", f"{base_url}/v0/management/usage/export", headers=mgmt_headers)
        assert_true(status == 200, f"usage export failed: {status}")
        assert_true(body.get("version") == 1, f"usage export version invalid: {body}")
        assert_true(int(body.get("usage", {}).get("total_requests", 0)) >= 1, "usage export total_requests invalid")

        status, _, text = request(
            "POST",
            f"{base_url}/v1/chat/completions",
            headers=client_headers,
            body={"model": "fail-model", "messages": [{"role": "user", "content": "hi"}], "stream": False},
        )
        assert_true(status >= 400, f"fail-model should fail, got {status}: {text}")

        def fail_disabled():
            status2, _, _, body2 = request_json(
                "GET",
                f"{base_url}/v0/management/openai-compatibility",
                headers=mgmt_headers,
            )
            assert_true(status2 == 200, f"reload openai-compatibility failed: {status2}")
            provider = next((item for item in body2.get("openai-compatibility", []) if item.get("name") == "fail-provider"), None)
            assert_true(provider is not None, "fail-provider missing after failure")
            key_entry = provider["api-key-entries"][0]
            assert_true(key_entry.get("disabled") is True, f"fail key not disabled: {key_entry}")
            return key_entry

        wait_until("status-rules disable", fail_disabled, timeout=20)

        def logs_ready():
            logs = [
                item
                for item in find_log_files(work_dir, auth_dir, Path.cwd() / "logs")
                if item.stat().st_mtime >= session_start - 5
            ]
            assert_true(bool(logs), "no request logs created")
            return logs

        logs = wait_until("request logs", logs_ready, timeout=15)
        request_log = next((item for item in logs if extract_request_id(item)), None)
        assert_true(request_log is not None, "request log with request id not found")
        request_id = extract_request_id(request_log)

        status, headers, _ = request(
            "GET",
            f"{base_url}/v0/management/request-log-by-id/{request_id}",
            headers=mgmt_headers,
            timeout=10,
        )
        assert_true(status == 200, f"request-log-by-id failed: {status}")
        disposition = headers.get("Content-Disposition", "")
        assert_true(request_id in disposition, "request-log-by-id returned unexpected file")

        stop_process(process)
        process = None
        stdout.close()
        stdout = None
        stderr.close()
        stderr = None

        process, stdout, stderr = start_binary(args.binary, config_path, output_dir)
        wait_until("healthz after restart", health_ready, timeout=40)

        status, _, _, body = request_json(
            "GET",
            f"{base_url}/v0/management/openai-compatibility",
            headers=mgmt_headers,
        )
        assert_true(status == 200, "openai-compatibility after restart failed")
        fail_provider_after_restart = next(
            item for item in body.get("openai-compatibility", []) if item.get("name") == "fail-provider"
        )
        assert_true(
            fail_provider_after_restart["api-key-entries"][0].get("disabled") is True,
            "disabled state not persisted after restart",
        )

        status, _, _, body = request_json(
            "PATCH",
            f"{base_url}/v0/management/openai-compatibility/runtime-state",
            headers=mgmt_headers,
            body={"auth-index": fail_auth_index, "action": "enable"},
        )
        assert_true(status == 200, f"runtime-state enable failed: {status} {body}")

        def fail_enabled():
            status2, _, _, body2 = request_json(
                "GET",
                f"{base_url}/v0/management/openai-compatibility",
                headers=mgmt_headers,
            )
            assert_true(status2 == 200, "reload after enable failed")
            provider = next((item for item in body2.get("openai-compatibility", []) if item.get("name") == "fail-provider"), None)
            assert_true(provider is not None, "fail-provider missing after enable")
            assert_true(provider["api-key-entries"][0].get("disabled") is False, "runtime enable did not take effect")
            return True

        wait_until("runtime-state enable", fail_enabled, timeout=15)

        summary = {
            "status": "ok",
            "config_path": str(config_path),
            "work_dir": str(work_dir),
            "log_files": [str(item) for item in logs],
        }
        (output_dir / "smoke-summary.json").write_text(json.dumps(summary, indent=2), encoding="utf-8")
        print(json.dumps(summary, ensure_ascii=False, indent=2))
        return 0
    except Exception as exc:
        failure = {
            "status": "failed",
            "error": str(exc),
            "work_dir": str(work_dir),
            "stdout_log": str(output_dir / "cpa.stdout.log"),
            "stderr_log": str(output_dir / "cpa.stderr.log"),
        }
        (output_dir / "smoke-summary.json").write_text(json.dumps(failure, indent=2), encoding="utf-8")
        print(json.dumps(failure, ensure_ascii=False, indent=2), file=sys.stderr)
        return 1
    finally:
        if stdout:
            stdout.close()
        if stderr:
            stderr.close()
        if process is not None:
            stop_process(process)
        mock_server.shutdown()
        mock_server.server_close()


if __name__ == "__main__":
    sys.exit(main())
