#!/usr/bin/env bash
# ARTEX 업데이트 스크립트: ① Docker 업데이트(소스로 이미지를 다시 빌드)  ② 로컬 컴파일 업데이트(바이너리 재빌드)
# install.sh 와 짝을 이룹니다. install 은 최초 설치를, update 는 새 버전으로의 업그레이드를 담당합니다.
# DB 마이그레이션은 직접 실행할 필요가 없습니다. artex 는 기동할 때마다 schema.sql 을 멱등하게 다시 돌리므로(ADD COLUMN/CREATE
# INDEX IF NOT EXISTS 포함) "재시작이 곧 마이그레이션"입니다. Compose의 DB·작업·인증 볼륨은 유지합니다.
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"

info(){ printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok(){   printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn(){ printf '\033[33m[!]\033[0m %s\n' "$*"; }
die(){  printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }
ask(){  local p="$1" d="${2:-}" a; read -rp "$p${d:+ [$d]}: " a; echo "${a:-$d}"; }

# ── 선택: 저장소를 최신 코드로 동기화합니다(compose·스크립트·로컬 컴파일 소스가 모두 이걸로 갱신됩니다) ───────
sync_repo(){
  git rev-parse --is-inside-work-tree >/dev/null 2>&1 && command -v git >/dev/null 2>&1 || { warn "git 작업 사본이 아니라 git pull 을 건너뜁니다"; return; }
  [ "$(ask '최신 코드를 받을까요 (git pull --ff-only)? (y/n)' y)" = y ] || return
  if ! git pull --ff-only; then
    warn "git pull 을 빨리 감기로 진행하지 못했습니다(로컬 변경이 있거나 브랜치가 갈라졌습니다). 직접 처리한 뒤 다시 시도하세요. 이번에는 현재 코드를 그대로 사용합니다"
  fi
}

# ── ① Docker 업데이트 ───────────────────────────────
update_docker(){
  command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1 || die "Docker와 Compose가 필요합니다"
  sync_repo
  docker compose up -d --build --wait
  ok "업데이트 완료: http://$(docker compose port artex 8787) (기존 데이터·인증 유지)"
}

# ── ② 로컬 컴파일 업데이트 ──────────────────────────────
update_local(){
  command -v go >/dev/null 2>&1 || die "Go 를 찾을 수 없습니다(>=1.26): https://go.dev/dl/"
  [ -f config.json ] || warn "config.json 을 찾을 수 없습니다. 최초 배포라면 ./install.sh 를 사용하세요"
  ok "Go: $(go version)"

  if command -v npm >/dev/null 2>&1; then
    info "프런트엔드 정적 산출물을 다시 빌드합니다…"
    ( cd web && npm ci && npm run build:static )
    rm -rf server/webui/dist && cp -r web/out server/webui/dist
    info "프런트엔드를 내장한 단일 바이너리를 다시 컴파일합니다…"
    CGO_ENABLED=0 go build -tags embedui -trimpath -o artex ./cmd/artex
  else
    warn "npm 을 찾을 수 없습니다. 프런트엔드를 내장하지 않은 백엔드만 컴파일합니다(프런트엔드는 npm run dev 로 따로 실행해야 합니다)"
    CGO_ENABLED=0 go build -o artex ./cmd/artex
  fi
  ok "컴파일 완료 → ./artex"
  warn "변경을 적용하려면 실행 중인 artex 프로세스를 재시작하세요(재시작 시 schema 를 자동으로 마이그레이션합니다)"
}

echo "=============================="
echo "  ARTEX 업데이트"
echo "  1) Docker Compose 업데이트"
echo "  2) 로컬 업데이트(go 로 다시 컴파일)"
echo "=============================="
case "$(ask '선택' 1)" in
  1) update_docker ;;
  2) sync_repo; update_local ;;
  *) die "잘못된 선택입니다" ;;
esac
