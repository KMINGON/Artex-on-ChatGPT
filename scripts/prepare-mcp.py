#!/usr/bin/env python3
"""독립 MCP 서버의 비공개 설정 준비와 ScopeSentry 공식 API 키 발급.

up은 네트워크 확인·설정 준비·서버 기동과 ScopeSentry 키 발급을 수행한다.
init은 파일만 준비하고 connect는 ScopeSentry 키를 발급·검증한다.
스캔과 ARTEX DB 변경은 수행하지 않는다.
"""

import argparse
import json
import os
from pathlib import Path
import re
import secrets
import shlex
import subprocess
import sys
import tempfile
from urllib.error import HTTPError
from urllib.request import HTTPRedirectHandler, ProxyHandler, Request, build_opener


ROOT = Path(__file__).resolve().parents[1]


class SetupError(Exception):
    pass


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def read_env(path):
    values = {}
    for line in path.read_text().splitlines():
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        key, separator, value = line.partition("=")
        if not separator or key.strip() in values:
            raise SetupError("환경 파일에 잘못되거나 중복된 항목이 있습니다.")
        parts = shlex.split(value, comments=True)
        if len(parts) > 1:
            raise SetupError("환경 값에 공백이 있으면 따옴표로 감싸세요.")
        values[key.strip()] = parts[0] if parts else ""
    return values


def create_private(path, contents):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as handle:
        handle.write(contents)


def docker(*args):
    # 키 발급에 읽는 파일과 실제 Compose 설정이 호스트 export 때문에 달라지지 않게 한다.
    env = {key: value for key, value in os.environ.items()
           if not key.upper().startswith(("COMPOSE_", "ARTEX_", "MSF_", "SCOPESENTRY_"))}
    env["COMPOSE_ANSI"] = "never"
    try:
        result = subprocess.run(["docker", *args], cwd=ROOT, env=env,
                                text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    except FileNotFoundError:
        raise SetupError("Docker를 찾지 못했습니다. Docker와 Compose를 먼저 설치하세요.") from None
    if result.returncode:
        # Docker 오류 본문에 설정·인증값이 포함될 수 있어 그대로 출력하지 않는다.
        raise SetupError(f"Docker {args[0]} 명령 실패(종료 코드 {result.returncode}). "
                         "Docker 실행 상태와 선택한 네트워크·Compose 설정을 확인하세요. 기존 파일은 보존합니다.")
    return result.stdout


def resolve_network(env_path, requested):
    saved = read_env(env_path).get("ARTEX_NETWORK") if env_path.exists() else None
    if env_path.exists() and not saved:
        raise SetupError("기존 환경 파일에 ARTEX_NETWORK가 없습니다. 사용할 네트워크를 파일에 지정하세요.")
    if saved and requested and saved != requested:
        raise SetupError("--network가 기존 ARTEX_NETWORK와 다릅니다. 환경 파일을 직접 수정한 뒤 다시 실행하세요.")
    network = saved or requested
    if not network:
        containers = docker("ps", "-q", "--filter", "label=com.docker.compose.service=artex").split()
        if len(containers) != 1:
            raise SetupError("실행 중인 ARTEX를 하나로 확인할 수 없습니다. ARTEX를 켜거나 --network <네트워크 이름>을 지정하세요.")
        networks = json.loads(docker("inspect", containers[0], "--format", "{{json .NetworkSettings.Networks}}"))
        candidates = [name for name in networks if name not in ("bridge", "host", "none")]
        if len(candidates) != 1:
            raise SetupError("ARTEX 네트워크를 하나로 확인할 수 없습니다. --network <네트워크 이름>을 지정하세요.")
        network = candidates[0]
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]*", network) or network in ("bridge", "host", "none"):
        raise SetupError("ARTEX가 연결된 사용자 정의 Docker 네트워크를 지정하세요.")
    docker("network", "inspect", network, "--format", "{{.Name}}")
    return network


def up(service, env_path, state_dir, network):
    network = resolve_network(env_path, network)
    if not env_path.exists():
        # 환경 파일만 잃어버린 설치의 DB에 새 암호를 연결하지 않는다.
        for kind in ("container", "volume"):
            ids = docker(kind, "ls", "-q", "--filter", f"label=com.docker.compose.project=artex-{service}",
                         *(["--all"] if kind == "container" else []))
            if ids.strip():
                raise SetupError("환경 파일 없이 같은 MCP 프로젝트의 Docker 자원이 남아 있습니다. 기존 환경 파일을 복원하세요.")
    initialize(service, env_path, state_dir, network)
    values = read_env(env_path)
    project = values.get("COMPOSE_PROJECT_NAME") or "artex-" + service
    print(f"{service} 서버를 준비합니다. 최초 이미지 다운로드와 초기화에는 시간이 걸릴 수 있습니다.", flush=True)
    docker("compose", "-p", project, "--env-file", str(env_path),
           "-f", str(ROOT / "docker" / service / "compose.yml"),
           "up", "-d", "--wait", "--wait-timeout", "300")
    if service == "scopesentry":
        connect(env_path)
        print("URL: http://scopesentry:8082/mcp")
        print("요청 헤더: X-API-Key=<환경 파일의 ARTEX_SCOPESENTRY_API_KEY 값>")
        print("Scanner 노드의 자체 검사·온라인 상태는 ScopeSentry에서 별도로 확인하세요.")
    else:
        print("URL: http://metasploit:3000/")
        print("요청 헤더: Authorization=Bearer <환경 파일의 MSF_MCP_AUTH_TOKEN 값>")
    print(f"환경 파일: {env_path}")
    print("ARTEX의 시스템 → MCP에서 입력하세요. 기존 설정과 사용 토글은 자동 변경하지 않습니다.")


def initialize(service, env_path, state_dir, network):
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]*", network):
        raise SetupError("ARTEX 네트워크 이름에 사용할 수 없는 문자가 있습니다.")
    if env_path.is_symlink():
        raise SetupError("환경 파일에 심볼릭 링크를 사용할 수 없습니다.")
    if env_path.exists():
        values = read_env(env_path)
        if service == "scopesentry":
            saved_state = Path(values.get("SCOPESENTRY_STATE_DIR", ""))
            if any(not (saved_state / name).is_file() for name in (
                "server-config.yaml", "server-password", "server-plugin-key",
            )) or not (saved_state / "server-plugin-key").stat().st_size:
                raise SetupError("기존 환경 파일의 ScopeSentry 설정 파일이 누락됐습니다. 기존 파일을 복원하세요.")
        print("기존 환경 파일과 네트워크 설정을 보존했습니다. 새 키나 비밀번호를 만들지 않았습니다.")
        return
    values = {
        "COMPOSE_PROJECT_NAME": "artex-" + service,
        "ARTEX_NETWORK": network,
    }
    env_path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    if service == "metasploit":
        values.update({
            "MSF_MCP_AUTH_TOKEN": secrets.token_hex(32),
            "MSF_POSTGRES_PASSWORD": secrets.token_hex(24),
            "MSF_MCP_DANGEROUS_ACTIONS": "true",
        })
    else:
        if state_dir.is_symlink():
            raise SetupError("설정 디렉터리에 심볼릭 링크를 사용할 수 없습니다.")
        state_dir = state_dir.resolve()
        # Compose와 아래 읽기 함수가 동일하게 해석할 수 있는 리터럴 경로만 쓴다.
        if any(character in str(state_dir) for character in "'\n\r"):
            raise SetupError("설정 디렉터리 경로에는 작은따옴표나 줄바꿈을 사용할 수 없습니다.")
        if state_dir.exists() and any(state_dir.iterdir()):
            raise SetupError("기존 설정 디렉터리가 있습니다. 기존 환경 파일을 사용하세요.")
        state_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
        state_dir.chmod(0o700)
        create_private(state_dir / "server-config.yaml", "")
        create_private(state_dir / "server-password", "")
        create_private(state_dir / "server-plugin-key", secrets.token_urlsafe(24))
        values.update({
            "SCOPESENTRY_STATE_DIR": str(state_dir),
            "SCOPESENTRY_PORT": "18083",
            "SCOPESENTRY_BIND_IP": "127.0.0.1",
            "SCOPESENTRY_NODE_NAME": "artex-scopesentry-node",
            "SCOPESENTRY_MONGO_USER": "scopesentry",
            "SCOPESENTRY_MONGO_PASSWORD": secrets.token_hex(24),
            "SCOPESENTRY_REDIS_PASSWORD": secrets.token_hex(24),
            "ARTEX_SCOPESENTRY_API_KEY": "",
        })
    create_private(env_path, "# 비공개 MCP 서버 설정. Git에 추가하지 마세요.\n" +
                   "".join(f"{key}='{value}'\n" for key, value in values.items()))
    print("MCP 서버 설정을 준비했습니다. 환경 파일과 상태 파일은 비공개로 보관하세요.")


def request(base, path, payload, headers=None):
    client = build_opener(ProxyHandler({}), NoRedirect())
    req = Request(base + path, data=json.dumps(payload).encode(), headers={
        "Content-Type": "application/json", **(headers or {}),
    })
    try:
        with client.open(req, timeout=60) as response:
            raw = response.read(1024 * 1024).decode()
            if "text/event-stream" in response.headers.get("Content-Type", ""):
                messages = [json.loads(line[5:].strip()) for line in raw.splitlines()
                            if line.startswith("data:")]
                return messages[-1] if messages else {}
            return json.loads(raw)
    except HTTPError as error:
        if error.code in (401, 403):
            raise SetupError("인증이 거부되었습니다. 기존 키·계정 설정을 확인하세요. 키는 자동 교체하지 않습니다.") from None
        raise SetupError("ScopeSentry 요청 실패: HTTP " + str(error.code)) from None


def save_api_key(env_path, key):
    lines = env_path.read_text().splitlines(keepends=True)
    updated = False
    for index, line in enumerate(lines):
        if line.partition("=")[0].strip() == "ARTEX_SCOPESENTRY_API_KEY":
            lines[index] = f"ARTEX_SCOPESENTRY_API_KEY='{key}'\n"
            updated = True
    if not updated:
        lines.append(f"\nARTEX_SCOPESENTRY_API_KEY='{key}'\n")
    fd, temporary = tempfile.mkstemp(dir=env_path.parent, prefix=".mcp-")
    try:
        with os.fdopen(fd, "w") as handle:
            handle.writelines(lines)
        os.replace(temporary, env_path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def connect(env_path):
    values = read_env(env_path)
    port = int(values.get("SCOPESENTRY_PORT", "18083"))
    host = values.get("SCOPESENTRY_BIND_IP", "127.0.0.1")
    if host == "0.0.0.0":
        host = "127.0.0.1"
    base = f"http://{host}:{port}"
    key = values.get("ARTEX_SCOPESENTRY_API_KEY", "")
    if not key:
        password = (Path(values["SCOPESENTRY_STATE_DIR"]) / "server-password").read_text().strip()
        if not password:
            raise SetupError("ScopeSentry 초기화가 끝나지 않았습니다. 서버 기동 후 다시 실행하세요.")
        login = request(base, "/api/user/login", {"username": "ScopeSentry", "password": password})
        token = login.get("data", {}).get("access_token")
        if not token:
            raise SetupError("초기 계정으로 로그인하지 못했습니다. 변경한 계정은 UI에서 API 키를 발급하세요.")
        result = request(base, "/api/apikey/create", {"name": "artex-scopesentry"},
                         {"Authorization": "Bearer " + token})
        key = result.get("data", {}).get("key", "")
        if not key.startswith("ssk_") or not key[4:].isalnum():
            raise SetupError("ScopeSentry가 정상 API 키를 반환하지 않았습니다.")
        # 검증이 일시 실패해도 재실행 시 이미 발급한 키를 재사용한다.
        save_api_key(env_path, key)
    result = request(base, "/mcp", {
        "jsonrpc": "2.0", "id": 1, "method": "initialize",
        "params": {"protocolVersion": "2025-06-18", "capabilities": {},
                   "clientInfo": {"name": "artex-mcp-setup", "version": "1"}},
    }, {"X-API-Key": key, "Accept": "application/json, text/event-stream"})
    if "error" in result or not result.get("result", {}).get("serverInfo"):
        raise SetupError("ScopeSentry MCP 초기화 응답을 확인하지 못했습니다.")
    print("ScopeSentry MCP 인증을 확인했습니다. ARTEX의 MCP 설정에서 URL과 API 키를 입력하고 토글을 켜세요.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("service", choices=("metasploit", "scopesentry"))
    parser.add_argument("action", choices=("up", "init", "connect"))
    parser.add_argument("--network", help="실행 중인 ARTEX 컨테이너의 Docker 네트워크 이름")
    parser.add_argument("--env-file", type=Path)
    parser.add_argument("--state-dir", type=Path, help="ScopeSentry 상태 파일 디렉터리")
    args = parser.parse_args()
    if args.action == "init" and not args.network:
        parser.error("init에는 --network가 필요합니다.")
    if args.service == "metasploit" and (args.action == "connect" or args.state_dir):
        parser.error("connect와 --state-dir는 ScopeSentry에서만 사용합니다.")
    if args.action == "connect" and (args.network or args.state_dir):
        parser.error("connect는 저장된 환경 파일의 네트워크와 상태 경로를 사용합니다.")
    private_dir = ROOT / ".local/mcp" / args.service
    env_path = args.env_file or private_dir / ".env"
    state_dir = args.state_dir or private_dir / "state"
    try:
        if env_path.is_symlink():
            raise SetupError("환경 파일에 심볼릭 링크를 사용할 수 없습니다.")
        if args.action == "up":
            up(args.service, env_path.absolute(), state_dir, args.network)
        elif args.action == "init":
            initialize(args.service, env_path.absolute(), state_dir, args.network)
        else:
            connect(env_path.absolute())
    except SetupError as error:
        print("준비 실패: " + str(error), file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        print("준비를 중단했습니다. 생성한 파일과 Docker 자원은 보존합니다.", file=sys.stderr)
        return 130
    except Exception as error:
        # 원본 응답·설정·인증값이 예외 메시지로 노출되지 않게 한다.
        print("준비 실패: " + type(error).__name__ + ". 파일 및 서버 상태를 확인하세요.", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
