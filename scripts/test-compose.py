#!/usr/bin/env python3
"""Smoke-test existing local images; never build, sign in to OpenAI, or infer.

Run from any directory: python3 scripts/test-compose.py
Requires Docker Compose v2 and locally available app and postgres:16-alpine
images. Set ARTEX_BUILD_VERSION to test a versioned image instead of :local.
Uses a fresh project and deletes only its volumes.
Command output, HTTP bodies, credentials, and authentication URLs are not logged.
"""

import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import signal
import socket
import subprocess
import sys
import uuid
from urllib.error import HTTPError, URLError
from urllib.request import HTTPRedirectHandler, ProxyHandler, Request, build_opener


ROOT = Path(__file__).resolve().parents[1]
MAX_BODY = 4 * 1024 * 1024


class SmokeFailure(Exception):
    pass


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        # No test request may follow an unexpected authentication/external URL.
        return None


def require(condition, reason):
    if not condition:
        raise SmokeFailure(reason)


def clean_environment():
    # --env-file also prevents Compose from loading this checkout's real .env.
    prefixes = ("ARTEX_", "POSTGRES_", "OPENAI_", "CHATGPT_", "CODEX_", "COMPOSE_")
    excluded = {"XDG_CONFIG_HOME", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY"}
    env = {
        key: value for key, value in os.environ.items()
        if not key.upper().startswith(prefixes) and key.upper() not in excluded
    }
    env["COMPOSE_ANSI"] = "never"
    return env


def command(args, env, label, timeout=60, data=None):
    try:
        result = subprocess.run(
            args, cwd=ROOT, env=env, input=data, stdout=subprocess.PIPE,
            stderr=subprocess.PIPE, timeout=timeout, check=False,
        )
    except FileNotFoundError:
        raise SmokeFailure(label + ": required executable not found") from None
    except subprocess.TimeoutExpired:
        raise SmokeFailure(label + ": timed out") from None
    if result.returncode:
        # Never echo captured diagnostics: tools may include configuration values.
        raise SmokeFailure(label + ": command exited " + str(result.returncode))
    return result.stdout


def parse_json(raw, label):
    try:
        return json.loads(raw)
    except (ValueError, UnicodeError):
        raise SmokeFailure(label + ": invalid JSON") from None


class Smoke:
    def __init__(self):
        self.env = clean_environment()
        self.build_version = os.environ.get("ARTEX_BUILD_VERSION", "")
        if self.build_version:
            self.env["ARTEX_BUILD_VERSION"] = self.build_version
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            self.port = sock.getsockname()[1]
        self.env["ARTEX_PORT"] = str(self.port)
        # Runtime smoke stays private; validate the public default separately.
        self.env["ARTEX_BIND_IP"] = "127.0.0.1"
        self.project = "artex-smoke-" + uuid.uuid4().hex
        self.compose_args = [
            "docker", "compose", "--project-name", self.project,
            "--project-directory", str(ROOT), "--env-file", "/dev/null",
            "--file", str(ROOT / "docker-compose.yml"),
        ]
        self.origin = "http://127.0.0.1:" + str(self.port)
        self.http = build_opener(ProxyHandler({}), NoRedirect())
        self.checks = 0
        self.cleanup_safe = False

    def compose(self, *args, label, timeout=60, data=None):
        return command(self.compose_args + list(args), self.env, label, timeout, data)

    def passed(self, label):
        self.checks += 1
        print("PASS: " + label, flush=True)

    def request(self, path, expected=200, method="GET", token=None, body=None, headers=None):
        request_headers = {"Origin": self.origin}
        if token:
            request_headers["Authorization"] = "Bearer " + token
        if body is not None:
            request_headers["Content-Type"] = "application/json"
            if not isinstance(body, bytes):
                body = json.dumps(body).encode()
        if headers:
            request_headers.update(headers)
        # None explicitly omits a header, allowing missing-Origin regression checks.
        request_headers = {k: v for k, v in request_headers.items() if v is not None}
        req = Request(self.origin + path, data=body, method=method, headers=request_headers)
        try:
            response = self.http.open(req, timeout=15)
        except HTTPError as error:
            response = error
        except (URLError, TimeoutError, OSError):
            raise SmokeFailure(method + " " + path + ": local HTTP connection failed") from None
        with response:
            status = response.status
            raw = response.read(MAX_BODY + 1)
            content_type = response.headers.get("Content-Type", "")
        require(status == expected, method + " " + path + ": expected HTTP " + str(expected) + ", got " + str(status))
        require(len(raw) <= MAX_BODY, path + ": response exceeded test limit")
        return raw, content_type

    def json_request(self, path, **kwargs):
        raw, _ = self.request(path, **kwargs)
        return parse_json(raw, path)

    def container(self, service):
        raw = self.compose("ps", "--quiet", service, label="find " + service + " container")
        ids = raw.decode().split()
        require(len(ids) == 1, service + ": expected one running container")
        return ids[0]

    def inspect(self, container):
        raw = command(["docker", "inspect", container], self.env, "inspect isolated container")
        return parse_json(raw, "container inspection")[0]

    def check_configuration(self):
        public_env = dict(self.env)
        public_env.pop("ARTEX_BIND_IP")
        raw = command(self.compose_args + ["config", "--format", "json"], public_env,
                      "validate default public web binding")
        public = parse_json(raw, "default Compose configuration")["services"]["artex"]
        require(public["ports"][0].get("host_ip") == "0.0.0.0",
                "default web binding does not accept LAN connections")
        hosts = public.get("extra_hosts", {})
        if isinstance(hosts, list):
            hosts = dict(item.replace(":", "=", 1).split("=", 1) for item in hosts)
        require(hosts.get("host.docker.internal") == "host-gateway",
                "Docker host access mapping is missing")
        raw = self.compose("config", "--format", "json", label="validate isolated Compose configuration")
        config = parse_json(raw, "Compose configuration")
        require(config.get("name") == self.project, "Compose project isolation was overridden")
        services = config.get("services", {})
        require(set(services) == {"artex", "postgres"}, "unexpected Compose services")
        volumes = config.get("volumes", {})
        require(set(volumes) == {"pgdata", "artex-data", "artex-auth"}, "unexpected persistent volumes")
        for volume in volumes.values():
            require(not volume.get("external") and volume.get("name", "").startswith(self.project + "_"),
                    "Compose references storage outside the isolated project")
        for network in config.get("networks", {}).values():
            require(not network.get("external") and network.get("name", "").startswith(self.project + "_"),
                    "Compose references a network outside the isolated project")
        for service in services.values():
            require(not service.get("network_mode") and not service.get("container_name"),
                    "Compose bypasses project network or container isolation")
            for mount in service.get("volumes", []):
                require(mount.get("type") == "volume" and mount.get("source") in volumes,
                        "Compose references a host path or unexpected volume")
        require(not services["postgres"].get("ports"), "Compose would publish the DB port")
        ports = services["artex"].get("ports", [])
        require(len(ports) == 1 and ports[0].get("host_ip") == "127.0.0.1"
                and str(ports[0].get("published")) == str(self.port) and ports[0].get("target") == 8787,
                "Compose would publish a port outside the selected loopback endpoint")
        require(not services["artex"].get("environment", {}).get("OPENAI_API_KEY"),
                "Compose would inject an API credential")
        self.images = [service["image"] for service in services.values()]
        self.cleanup_safe = True

    def check_bindings(self):
        app = self.inspect(self.container("artex"))
        database = self.inspect(self.container("postgres"))
        for info in (app, database):
            require(info["Config"]["Labels"].get("com.docker.compose.project") == self.project,
                    "container belongs to a different Compose project")
        require(not any(database["NetworkSettings"]["Ports"].values()), "DB has a published host port")
        bindings = app["NetworkSettings"]["Ports"]
        require(bindings.get("8787/tcp") == [{"HostIp": "127.0.0.1", "HostPort": str(self.port)}],
                "web port is not bound exclusively to the selected loopback port")
        require(not any(value for key, value in bindings.items() if key != "8787/tcp"),
                "unexpected additional app host port")
        for info, destinations in ((app, ("/app/data", "/app/auth")), (database, ("/var/lib/postgresql/data",))):
            mounts = {mount["Destination"]: mount for mount in info["Mounts"]}
            for dest in destinations:
                mount = mounts.get(dest, {})
                require(mount.get("Type") == "volume" and mount.get("Name", "").startswith(self.project + "_"),
                        "persistent storage does not belong to the isolated test project")
        app_env = dict(item.split("=", 1) for item in app["Config"]["Env"] if "=" in item)
        require(not app_env.get("OPENAI_API_KEY"), "app unexpectedly has an API credential")
        require(app_env.get("ARTEX_AUTH_DIR") == "/app/auth" and app_env.get("XDG_CONFIG_HOME") == "/app/auth",
                "authentication storage is not configured on its dedicated volume")
        self.passed("isolated volumes; DB unpublished; web bound only to loopback")

    def snapshot(self):
        # Hash inside the container: no key, host ID, or account data leaves it.
        script = """
import hashlib, json, pathlib, stat
auth = pathlib.Path('/app/auth')
store = auth / 'artex-on-chatgpt'
state = json.loads((store / 'accounts.json').read_bytes())
assert state['version'] == 1 and state['accounts'] == {}
assert state['host_id'].startswith('urn:uuid:')
assert stat.S_IMODE(store.stat().st_mode) == 0o700
for path in (auth / 'jwt.key', store / 'accounts.json'):
    assert path.is_file() and stat.S_IMODE(path.stat().st_mode) == 0o600
key = (auth / 'jwt.key').read_bytes()
assert key
def digest(value):
    return hashlib.sha256(value).hexdigest()
print(json.dumps({'jwt': digest(key), 'host': digest(state['host_id'].encode()),
                 'file': digest(pathlib.Path('/app/data/compose-smoke/workfile.txt').read_bytes())}))
"""
        raw = self.compose("exec", "-T", "artex", "python3", "-c", script, label="inspect test persistence")
        return parse_json(raw, "test persistence snapshot")

    def run(self):
        self.check_configuration()
        for image in self.images:
            command(["docker", "image", "inspect", image], self.env, "required local image " + image)
        self.compose("up", "-d", "--no-build", "--pull", "never", "--wait", "--wait-timeout", "180",
                     label="start isolated Compose project", timeout=240)
        self.check_bindings()
        health = self.json_request("/api/health")
        require(health.get("ok") is True and health.get("service") == "artex", "health response is not ARTEX")
        require(health.get("version") == (self.build_version or "docker-local"),
                "running app version does not match the selected build")
        html, content_type = self.request("/")
        require("text/html" in content_type and b"<html" in html.lower(), "embedded UI HTML is missing")
        asset = re.search(rb'(?:src|href)="(/_next/static/[^"?#]+\.js)"', html)
        require(asset is not None, "embedded UI JavaScript reference is missing")
        js, _ = self.request(asset.group(1).decode())
        require(bool(js) and b"<html" not in js[:100].lower(), "embedded UI JavaScript is missing")
        self.passed("health endpoint and embedded UI assets")
        status = self.json_request("/api/auth/status")
        require(status.get("initialized") is True and status.get("setup_token_required") is False,
                "fresh DB did not initialize the default administrator")
        login = self.json_request("/api/auth/login", method="POST", body={"username": "ARTEX", "password": "artex1234"})
        token = login.get("token")
        require(isinstance(token, str) and token, "administrator login did not return a token")
        for route in ("apply", "rollback"):
            self.request("/api/update/" + route, method="POST", expected=409, token=token)
        self.passed("Docker rejects binary update and rollback before external requests")
        # Check the real DB/API defaults, including editable prompt bodies and
        # nested tool descriptions that static page translation cannot cover.
        agents = self.json_request("/api/agents", token=token).get("agents", [])
        require(len(agents) >= 6, "built-in agents were not seeded")
        defaults = [self.json_request("/api/agents/" + item["key"], token=token) for item in agents]
        for path in ("/api/tools", "/api/intercept/rules", "/api/asset-intercept/rules",
                     "/api/intercept/judge", "/api/skills"):
            defaults.append(self.json_request(path, token=token))
        text = json.dumps(defaults, ensure_ascii=False)
        require(re.search(r"[\u3400-\u9fff\uf900-\ufaff]", text) is None,
                "fresh installation exposes Chinese in supplied defaults")
        require(re.search(r"[가-힣]", text) is not None, "Korean defaults are missing")
        self.passed("Korean agent, tool, interception and skill defaults through the real API")
        state = self.json_request("/api/chatgpt/status", token=token)
        require(state.get("accounts") == [] and state.get("pending") is False, "test account store is not empty")
        self.passed("first-run setup complete; default administrator login; empty account store")

        for route in ("login", "logout", "recheck"):
            path = "/api/chatgpt/" + route
            # Invalid JSON is deliberate: even a broken guard cannot initiate OAuth.
            self.request(path, method="POST", expected=401, body=b"{")
            self.request(path, method="POST", expected=401, token="invalid-smoke-token", body=b"{")
            for headers in ({"Origin": None}, {"Origin": "http://invalid.example"}, {"Host": "invalid.example"}):
                self.request(path, method="POST", expected=403, token=token, body=b"{", headers=headers)
            self.request(path, method="POST", expected=400, token=token, body=b"{")
        self.request("/auth/callback", expected=404)
        self.passed("ChatGPT mutations require JWT and exact Origin/Host; idle callback returns 404")

        changed_password = secrets.token_urlsafe(24)
        changed = self.json_request("/api/auth/change-password", method="POST", token=token,
                                    body={"old_password": "artex1234", "new_password": changed_password})
        replacement = changed.get("token")
        require(isinstance(replacement, str) and replacement, "password change did not return a fresh session")
        self.request("/api/chatgpt/status", expected=401, token=token)
        self.request("/api/chatgpt/status", token=replacement)
        marker = secrets.token_bytes(32)
        self.compose("exec", "-T", "artex", "python3", "-c",
                     "import pathlib,sys; p=pathlib.Path('/app/data/compose-smoke/workfile.txt'); "
                     "p.parent.mkdir(exist_ok=True); p.write_bytes(sys.stdin.buffer.read())",
                     label="write isolated work file", data=marker)
        before = self.snapshot()
        require(before.get("file") == hashlib.sha256(marker).hexdigest(), "work file was not written correctly")
        previous_container = self.container("artex")
        self.compose("up", "-d", "--no-build", "--pull", "never", "--force-recreate", "--no-deps", "--wait",
                     "--wait-timeout", "180", "artex", label="recreate app container", timeout=240)
        require(self.container("artex") != previous_container, "app container was not recreated")
        self.check_bindings()
        require(self.json_request("/api/auth/status").get("initialized") is True,
                "administrator setup was lost after recreation")
        self.request("/api/auth/login", method="POST", expected=401,
                     body={"username": "ARTEX", "password": "artex1234"})
        login = self.json_request("/api/auth/login", method="POST", body={"username": "ARTEX", "password": changed_password})
        require(isinstance(login.get("token"), str) and login["token"], "saved administrator password no longer logs in")
        state = self.json_request("/api/chatgpt/status", token=replacement)
        require(state.get("accounts") == [] and state.get("pending") is False, "account store changed during recreation")
        require(self.snapshot() == before, "work file, JWT signing key, or account host identity changed after recreation")
        self.passed("saved password, work file, JWT key, account host identity, and pre-recreation session persist")


def main():
    smoke = Smoke()
    result = 0
    # Give Ctrl+C/normal termination the same project-scoped cleanup path.
    def interrupted(_signum, _frame):
        raise KeyboardInterrupt
    signal.signal(signal.SIGTERM, interrupted)
    try:
        smoke.run()
    except SmokeFailure as error:
        print("FAIL: " + str(error), file=sys.stderr)
        result = 1
    except KeyboardInterrupt:
        print("FAIL: interrupted", file=sys.stderr)
        result = 130
    except Exception as error:
        # Avoid exception contents that could contain a response or command data.
        print("FAIL: unexpected " + type(error).__name__, file=sys.stderr)
        result = 1
    finally:
        if smoke.cleanup_safe:
            try:
                smoke.compose("down", "--volumes", "--remove-orphans", label="clean up isolated Compose project", timeout=90)
                print("PASS: isolated containers and volumes removed", flush=True)
            except (SmokeFailure, KeyboardInterrupt):
                print("FAIL: cleanup incomplete for project " + smoke.project, file=sys.stderr)
                result = 1
    print("Checks passed: " + str(smoke.checks) + "; no OpenAI sign-in or inference requested.", flush=True)
    return result


if __name__ == "__main__":
    sys.exit(main())
