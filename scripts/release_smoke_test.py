#!/usr/bin/env python3
import argparse
import json
import os
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import textwrap
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


CLIENT_API_KEY = "client-test-key"
LOCAL_PASSWORD = "local-test-password"
SUCCESS_UPSTREAM_KEY = "upstream-good-key"
FAIL_UPSTREAM_KEY = "upstream-bad-key"


class CheckFailed(RuntimeError):
    pass


def free_port():
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


class MockHandler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        return

    def _send(self, status, body, content_type="application/json"):
        raw = body.encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_GET(self):
        auth = self.headers.get("Authorization", "")
        if self.path == "/success/v1/ping":
            if auth != f"Bearer {SUCCESS_UPSTREAM_KEY}":
                self._send(401, "bad upstream auth", "text/plain")
                return
            self._send(200, json.dumps({"ok": True, "path": self.path}))
            return
        self._send(404, "not found", "text/plain")

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0") or "0")
        payload = self.rfile.read(length).decode("utf-8") if length > 0 else ""
        auth = self.headers.get("Authorization", "")

        if self.path == "/success/v1/chat/completions":
            if auth != f"Bearer {SUCCESS_UPSTREAM_KEY}":
                self._send(401, "bad upstream auth", "text/plain")
                return
            body = {
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
            }
            self._send(200, json.dumps(body))
            return

        if self.path == "/fail/v1/chat/completions":
            if auth != f"Bearer {FAIL_UPSTREAM_KEY}":
                self._send(401, "unauthorized", "text/plain")
                return
            self._send(401, "unauthorized", "text/plain")
            return

        self._send(404, "not found", "text/plain")


def request_json(method, url, *, headers=None, payload=None, timeout=20):
    req = urllib.request.Request(url, method=method, headers=headers or {})
    data = None
    if payload is not None:
        data = json.dumps(payload).encode("utf-8")
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, data=data, timeout=timeout) as resp:
            text = resp.read().decode("utf-8")
            return resp.status, text, json.loads(text) if text else {}
    except urllib.error.HTTPError as exc:
        text = exc.read().decode("utf-8", errors="replace")
        body = None
        try:
            body = json.loads(text) if text else {}
        except Exception:
            body = text
        return exc.code, text, body


def request_text(method, url, *, headers=None, payload=None, timeout=20):
    req = urllib.request.Request(url, method=method, headers=headers or {})
    data = None
    if payload is not None:
        data = payload.encode("utf-8")
    try:
        with urllib.request.urlopen(req, data=data, timeout=timeout) as resp:
            return resp.status, resp.read().decode("utf-8", errors="replace")
    except urllib.error.HTTPError as exc:
        return exc.code, exc.read().decode("utf-8", errors="replace")


def wait_until(name, fn, timeout=30, interval=0.5):
    deadline = time.time() + timeout
    last_error = None
    while time.time() < deadline:
        try:
            return fn()
        except Exception as exc:
            last_error = exc
            time.sleep(interval)
    raise CheckFailed(f"{name} 超时: {last_error}")


def assert_true(condition, message):
    if not condition:
        raise CheckFailed(message)


def build_config(port, mock_port, auth_dir):
    return textwrap.dedent(
        f"""
        host: "127.0.0.1"
        port: {port}
        auth-dir: "{auth_dir}"
        request-log: true
        usage-statistics-enabled: true
        logging-to-file: false
        api-keys:
          - "{CLIENT_API_KEY}"
        proxy-url: "http://127.0.0.1:9"
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
              - name: "401 unauthorized 停用"
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


def start_binary(binary_path, config_path, management_html, output_dir):
    static_dir = Path(output_dir) / "static"
    static_dir.mkdir(parents=True, exist_ok=True)
    target_html = static_dir / "management.html"
    shutil.copyfile(management_html, target_html)

    env = os.environ.copy()
    env["MANAGEMENT_STATIC_PATH"] = str(target_html)
    env["MANAGEMENT_PASSWORD"] = ""

    stdout_path = Path(output_dir) / "cpa.stdout.log"
    stderr_path = Path(output_dir) / "cpa.stderr.log"
    stdout = open(stdout_path, "w", encoding="utf-8")
    stderr = open(stderr_path, "w", encoding="utf-8")
    process = subprocess.Popen(
        [str(binary_path), "-config", str(config_path), "-password", LOCAL_PASSWORD],
        stdout=stdout,
        stderr=stderr,
        env=env,
    )
    return process, stdout, stderr


def stop_process(process):
    if process.poll() is not None:
        return
    process.terminate()
    try:
        process.wait(timeout=10)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=10)


def find_logs(base_dir):
    paths = []
    for path in Path(base_dir).rglob("*.log"):
        if path.name in {"cpa.stdout.log", "cpa.stderr.log"}:
            continue
        paths.append(path)
    return sorted(paths)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", required=True)
    parser.add_argument("--management-html", required=True)
    parser.add_argument("--output-dir", required=True)
    args = parser.parse_args()

    output_dir = Path(args.output_dir).resolve()
    output_dir.mkdir(parents=True, exist_ok=True)

    mock_port = free_port()
    cpa_port = free_port()
    work_dir = Path(tempfile.mkdtemp(prefix="cpa-smoke-", dir=str(output_dir)))
    auth_dir = work_dir / "auths"
    auth_dir.mkdir(parents=True, exist_ok=True)
    config_path = work_dir / "config.yaml"
    config_path.write_text(build_config(cpa_port, mock_port, auth_dir), encoding="utf-8")

    mock_server = ThreadingHTTPServer(("127.0.0.1", mock_port), MockHandler)
    mock_thread = threading.Thread(target=mock_server.serve_forever, daemon=True)
    mock_thread.start()

    process = None
    stdout = None
    stderr = None

    mgmt_headers = {"X-Management-Key": LOCAL_PASSWORD}
    client_headers = {"Authorization": f"Bearer {CLIENT_API_KEY}", "Content-Type": "application/json"}
    base_url = f"http://127.0.0.1:{cpa_port}"

    try:
        process, stdout, stderr = start_binary(args.binary, config_path, args.management_html, output_dir)

        def health_ready():
            if process.poll() is not None:
                raise CheckFailed(f"CPA 进程提前退出，退出码 {process.returncode}")
            status, text = request_text("GET", f"{base_url}/healthz", timeout=5)
            assert_true(status == 200, f"/healthz 返回 {status}: {text}")
            return True

        wait_until("healthz", health_ready, timeout=40)

        status, text = request_text("GET", f"{base_url}/management.html", timeout=10)
        assert_true(status == 200 and "<html" in text.lower(), "management.html 未正常返回 HTML")

        status, _, body = request_json("GET", f"{base_url}/v0/management/openai-compatibility", headers=mgmt_headers)
        assert_true(status == 200, f"读取 openai-compatibility 失败: {status}")
        providers = body.get("openai-compatibility", [])
        assert_true(len(providers) == 2, f"提供商数量异常: {len(providers)}")

        success_provider = next((item for item in providers if item.get("name") == "success-provider"), None)
        fail_provider = next((item for item in providers if item.get("name") == "fail-provider"), None)
        assert_true(success_provider is not None, "缺少 success-provider")
        assert_true(fail_provider is not None, "缺少 fail-provider")
        success_auth_index = success_provider["api-key-entries"][0].get("auth-index", "")
        fail_auth_index = fail_provider["api-key-entries"][0].get("auth-index", "")
        assert_true(bool(success_auth_index), "success-provider 缺少 auth-index")
        assert_true(bool(fail_auth_index), "fail-provider 缺少 auth-index")

        status, _, body = request_json(
            "POST",
            f"{base_url}/v0/management/api-call",
            headers=mgmt_headers,
            payload={
                "authIndex": success_auth_index,
                "method": "GET",
                "url": f"http://127.0.0.1:{mock_port}/success/v1/ping",
                "header": {"Authorization": "Bearer $TOKEN$"},
            },
        )
        assert_true(status == 200, f"management api-call 调用失败: {status}")
        assert_true(body.get("status_code") == 200, f"management api-call 上游状态异常: {body}")

        status, text = request_text(
            "POST",
            f"{base_url}/v1/chat/completions",
            headers=client_headers,
            payload=json.dumps(
                {"model": "ok-model", "messages": [{"role": "user", "content": "hi"}], "stream": False}
            ),
        )
        assert_true(status == 200, f"ok-model 请求失败: {status} {text}")
        ok_body = json.loads(text)
        assert_true(ok_body["choices"][0]["message"]["content"] == "ok", "ok-model 返回内容异常")

        def usage_ready():
            status2, _, body2 = request_json("GET", f"{base_url}/v0/management/usage", headers=mgmt_headers)
            assert_true(status2 == 200, f"读取 usage 失败: {status2}")
            usage = body2.get("usage", {})
            assert_true(int(usage.get("total_requests", 0)) >= 1, f"usage.total_requests 异常: {usage}")
            return usage

        usage = wait_until("usage 统计", usage_ready, timeout=15)
        assert_true("apis" in usage, "usage 快照缺少 apis 字段")

        status, _, body = request_json(
            "GET",
            f"{base_url}/v0/management/usage/export",
            headers=mgmt_headers,
        )
        assert_true(status == 200 and "usage" in body, "usage export 失败")

        status, text = request_text(
            "POST",
            f"{base_url}/v1/chat/completions",
            headers=client_headers,
            payload=json.dumps(
                {"model": "fail-model", "messages": [{"role": "user", "content": "hi"}], "stream": False}
            ),
        )
        assert_true(status >= 400, f"fail-model 本应失败，实际返回 {status}: {text}")

        def fail_disabled():
            status2, _, body2 = request_json("GET", f"{base_url}/v0/management/openai-compatibility", headers=mgmt_headers)
            assert_true(status2 == 200, f"重读 openai-compatibility 失败: {status2}")
            items = body2.get("openai-compatibility", [])
            provider = next((item for item in items if item.get("name") == "fail-provider"), None)
            assert_true(provider is not None, "重读时缺少 fail-provider")
            key_entry = provider["api-key-entries"][0]
            assert_true(key_entry.get("disabled") is True, f"失败 key 未被自动停用: {key_entry}")
            return key_entry

        fail_key_state = wait_until("status-rules 自动停用", fail_disabled, timeout=20)
        assert_true(fail_key_state.get("status") in {"disabled", "error", "unavailable", ""}, "失败 key 状态字段异常")

        stop_process(process)
        process = None
        if stdout:
            stdout.close()
            stdout = None
        if stderr:
            stderr.close()
            stderr = None

        process, stdout, stderr = start_binary(args.binary, config_path, args.management_html, output_dir)
        wait_until("重启后的 healthz", health_ready, timeout=40)

        status, _, body = request_json("GET", f"{base_url}/v0/management/openai-compatibility", headers=mgmt_headers)
        assert_true(status == 200, "重启后读取 openai-compatibility 失败")
        fail_provider_after_restart = next(
            item for item in body.get("openai-compatibility", []) if item.get("name") == "fail-provider"
        )
        assert_true(
            fail_provider_after_restart["api-key-entries"][0].get("disabled") is True,
            "重启后停用状态未持久化",
        )

        status, _, body = request_json(
            "PATCH",
            f"{base_url}/v0/management/openai-compatibility/runtime-state",
            headers=mgmt_headers,
            payload={"auth-index": fail_auth_index, "action": "enable"},
        )
        assert_true(status == 200, f"runtime-state enable 失败: {status} {body}")

        def fail_enabled():
            status2, _, body2 = request_json("GET", f"{base_url}/v0/management/openai-compatibility", headers=mgmt_headers)
            assert_true(status2 == 200, "enable 后读取 openai-compatibility 失败")
            provider = next(
                item for item in body2.get("openai-compatibility", []) if item.get("name") == "fail-provider"
            )
            assert_true(provider["api-key-entries"][0].get("disabled") is False, "runtime-state enable 未生效")
            return True

        wait_until("runtime-state enable", fail_enabled, timeout=15)

        logs = find_logs(work_dir)
        assert_true(bool(logs), "未生成任何请求日志文件")

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
