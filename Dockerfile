# syntax=docker/dockerfile:1
# Compose builds the app and frontend from this checkout.
FROM --platform=$BUILDPLATFORM node:24-bookworm-slim AS node

FROM node AS web-build
WORKDIR /src/web
ENV NEXT_TELEMETRY_DISABLED=1 HUSKY=0
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY web/ ./
RUN npm run build:static

FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS go-build
WORKDIR /src
COPY go.mod go.sum ./
COPY third_party/norma/go.mod third_party/norma/go.sum ./third_party/norma/
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
COPY --from=web-build /src/web/out ./server/webui/dist
ARG TARGETOS
ARG TARGETARCH
ARG ARTEX_BUILD_VERSION=docker-local
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -tags embedui -trimpath -ldflags="-s -w -X main.version=${ARTEX_BUILD_VERSION}" -o /out/artex ./cmd/artex

FROM python:3.12-slim-bookworm AS runtime
# Keep the existing local tool set available inside the container.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates libnss3-tools ripgrep curl wget vim git jq unzip \
      dnsutils iputils-ping netcat-openbsd inetutils-telnet whois nmap \
    && rm -rf /var/lib/apt/lists/*
# Runtime Node must match the target architecture, unlike the frontend builder.
COPY --from=node:24-bookworm-slim /usr/local/bin/node /usr/local/bin/node
COPY --from=node:24-bookworm-slim /usr/local/lib/node_modules/npm /usr/local/lib/node_modules/npm
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx \
    && npm install --prefix /opt/artex-browser @playwright/mcp@0.0.83 @playwright/cli@0.1.22 playwright@1.64.0-alpha-1790635538000 \
    && ln -s /opt/artex-browser/node_modules/.bin/playwright-cli /usr/local/bin/playwright-cli \
    && ln -s /opt/artex-browser/node_modules/.bin/playwright /usr/local/bin/playwright \
    && PLAYWRIGHT_BROWSERS_PATH=/opt/artex-browser/browsers playwright install --with-deps chromium \
    && PLAYWRIGHT_BROWSERS_PATH=/opt/artex-browser/browsers node -e 'require("fs").symlinkSync(require("/opt/artex-browser/node_modules/playwright").chromium.executablePath(), "/usr/local/bin/artex-chromium")' \
    && npm cache clean --force \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY start.sh /app/start.sh
COPY skills/ /app/skills/
COPY scripts/browser-session.cjs scripts/browser-mcp.cjs scripts/browser-runtime.cjs /app/scripts/
COPY scripts/browser-runtime.cjs /opt/artex-browser/runtime.cjs
COPY LICENSE /app/LICENSE
COPY web/LICENSE /app/WEB-LICENSE
RUN chmod +x /app/start.sh && mkdir -p /app/auth && chmod 700 /app/auth
ENV ARTEX_AUTH_DIR=/app/auth XDG_CONFIG_HOME=/app/auth ARTEX_LANG=ko
ENV NODE_PATH=/opt/artex-browser/node_modules PLAYWRIGHT_BROWSERS_PATH=/opt/artex-browser/browsers \
    CHROMIUM=/usr/local/bin/artex-chromium PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH=/usr/local/bin/artex-chromium
VOLUME ["/app/data", "/app/auth"]
EXPOSE 8787
ENTRYPOINT ["/app/start.sh"]
CMD ["-addr", ":8787", "-proxy", "127.0.0.1:8788"]

FROM runtime AS app
COPY --from=go-build --chmod=755 /out/artex /app/artex
