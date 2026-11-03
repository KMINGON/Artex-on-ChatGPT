#!/usr/bin/env python3
"""현재 소스의 ARTEX·PostgreSQL·Juice Shop 준비와 프로젝트 한정 정리."""

import argparse
import json
import os
from pathlib import Path
import secrets
import shutil
import subprocess
import sys


ROOT = Path(__file__).resolve().parents[1]
STATE = ROOT / ".local" / "dev-lab"
ENV_FILE = STATE / ".env"
PROJECT = "artex-dev-lab"
IMAGE = "artex-on-chatgpt:dev-lab"
OWNER = "com.artex.dev-lab.root"


class LabError(Exception):
    pass


def environment():
    # 호스트의 실제 계정·다른 Compose 설정을 실습 환경에 암묵적으로 주입하지 않는다.
    prefixes = ("ARTEX_", "POSTGRES_", "OPENAI_", "CHATGPT_", "CODEX_", "COMPOSE_", "JUICESHOP_")
    env = {k: v for k, v in os.environ.items() if not k.upper().startswith(prefixes)}
    env.update(ARTEX_DEV_LAB_ROOT=str(ROOT), COMPOSE_ANSI="never")
    return env


def docker(*args, capture=True):
    result = subprocess.run(
        ["docker", *args], cwd=ROOT, env=environment(), text=True,
        stdout=subprocess.PIPE if capture else None,
        stderr=subprocess.PIPE if capture else None,
    )
    if result.returncode:
        raise LabError(f"Docker 명령 실패({args[0]}), 종료 코드 {result.returncode}. 환경 파일을 보존했습니다.")
    return result.stdout or ""


def resources(kind):
    ids = docker(kind, "ls", "-q", "--filter", f"label=com.docker.compose.project={PROJECT}",
                 *(["--all", "--no-trunc"] if kind == "container" else [])).split()
    objects = json.loads(docker(kind, "inspect", *ids)) if ids else []
    for item in objects:
        labels = item["Config"].get("Labels") if kind == "container" else item.get("Labels")
        if (labels or {}).get(OWNER) != str(ROOT):
            raise LabError(f"같은 프로젝트명의 다른 환경이 있습니다({kind}). 변경하지 않았습니다.")
    return ids


def check_state_path():
    if STATE.is_symlink() or ENV_FILE.is_symlink() or not STATE.parent.resolve().is_relative_to(ROOT):
        raise LabError("개발 환경 경로에 심볼릭 링크를 사용할 수 없습니다.")


def owned_image_exists():
    images = docker("image", "ls", "-q", "--filter", f"reference={IMAGE}").split()
    if images:
        image = json.loads(docker("image", "inspect", IMAGE))[0]
        if (image.get("Config", {}).get("Labels") or {}).get(OWNER) != str(ROOT):
            raise LabError("전용 이미지의 저장소 경로가 다릅니다. 변경하지 않았습니다.")
    return bool(images)


def compose(*args, capture=True):
    return docker("compose", "--project-name", PROJECT, "--env-file", str(ENV_FILE),
                  "-f", "docker-compose.yml", "-f", "docker-compose.dev.yml", *args, capture=capture)


def initialize():
    check_state_path()
    existing = {kind: resources(kind) for kind in ("container", "volume", "network")}
    if ENV_FILE.exists():
        return
    if any(existing.values()):
        raise LabError("환경 파일 없이 개발 Docker 자원이 남아 있습니다. 환경 파일을 복원하거나 clean으로 정리하세요.")
    STATE.mkdir(parents=True, exist_ok=True, mode=0o700)
    STATE.chmod(0o700)
    values = {
        "ARTEX_BUILD_VERSION": "dev-lab",
        "ARTEX_BIND_IP": "127.0.0.1",
        "ARTEX_PORT": "8788",
        "JUICESHOP_PORT": "3000",
        "POSTGRES_USER": "artex",
        "POSTGRES_DB": "artex",
        "POSTGRES_PASSWORD": secrets.token_hex(24),
        "ARTEX_ADMIN_PASSWORD": secrets.token_hex(16),
        "OPENAI_API_KEY": "",
        "ARTEX_LLM_MODEL": "",
        "ARTEX_LLM_PROXY": "",
    }
    fd = os.open(ENV_FILE, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as handle:
        handle.write("# 비공개 개발 환경. up은 저장된 값을 보존하고 clean은 이 파일을 삭제합니다.\n")
        handle.writelines(f"{key}='{value}'\n" for key, value in values.items())


def up():
    owned_image_exists()
    initialize()
    config = json.loads(compose("config", "--format", "json"))
    if set(config["services"]) != {"artex", "postgres", "juiceshop"}:
        raise LabError("예상하지 않은 서비스가 개발 환경에 포함되어 있습니다.")
    for service in ("artex", "juiceshop"):
        ports = config["services"][service].get("ports", [])
        if len(ports) != 1 or ports[0].get("host_ip") != "127.0.0.1":
            raise LabError("개발 환경은 서비스별 loopback 포트 하나만 사용합니다.")
    compose("up", "-d", "--build", "--wait", "--wait-timeout", "180", capture=False)
    for service in ("artex", "juiceshop"):
        port = config["services"][service]["ports"][0]["published"]
        print(f"{service}: http://127.0.0.1:{port}")
    print(f"관리자 계정: ARTEX / 비밀번호: {ENV_FILE}의 ARTEX_ADMIN_PASSWORD")
    print("ARTEX 내부 테스트 대상: http://juiceshop:3000")


def clean():
    check_state_path()
    targets = {kind: resources(kind) for kind in ("container", "volume", "network")}
    for network in targets["network"]:
        attached = docker("container", "ls", "--all", "--no-trunc", "-q",
                          "--filter", f"network={network}").split()
        if set(attached) - set(targets["container"]):
            raise LabError("개발 네트워크에 다른 프로젝트의 컨테이너가 연결되어 있습니다. "
                           "외부 MCP 프로젝트를 먼저 down한 뒤 clean을 다시 실행하세요. "
                           "ARTEX 자원은 변경하지 않았습니다.")
    images = owned_image_exists()
    if targets["container"]:
        docker("stop", "--time", "30", *targets["container"])
        docker("container", "rm", "--volumes", *targets["container"])
    for kind in ("volume", "network"):
        if targets[kind]:
            docker(kind, "rm", *targets[kind])
    if images:
        # 다른 컨테이너에서 사용 중이면 강제 삭제하지 않고 오류를 반환한다.
        docker("image", "rm", IMAGE)
    if STATE.exists():
        shutil.rmtree(STATE)
    print("개발 컨테이너·볼륨·네트워크·전용 앱 이미지·로컬 실행 데이터 정리 완료")
    print("공용 PostgreSQL/Juice Shop 이미지와 빌드 캐시, 다른 프로젝트는 보존했습니다.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("up", "status", "clean"),
                        help="up: 준비·기동, status: 상태, clean: 작업·인증 데이터까지 영구 삭제")
    args = parser.parse_args()
    try:
        if args.action == "up":
            up()
        elif args.action == "clean":
            clean()
        else:
            resources("container")
            print(docker("ps", "--all", "--filter", f"label=com.docker.compose.project={PROJECT}",
                         "--format", "table {{.Names}}\t{{.Status}}\t{{.Ports}}"), end="")
    except (LabError, OSError, ValueError) as error:
        print(f"오류: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
