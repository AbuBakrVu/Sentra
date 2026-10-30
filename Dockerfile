# --- интерфейс ---
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# --- агенты и сервер ---
FROM golang:1.24-alpine AS go
RUN apk add --no-cache gcc musl-dev
WORKDIR /src
COPY go.mod go.sum ./
COPY vendor/ vendor/
COPY cmd/ cmd/
COPY internal/ internal/
COPY web/embed.go web/embed.go
COPY --from=web /src/web/dist web/dist
ARG VERSION=0.1.0
# агенты собираем без cgo, под обе архитектуры
RUN for a in amd64 arm64; do \
      CGO_ENABLED=0 GOOS=linux GOARCH=$a go build -mod=vendor -trimpath \
        -ldflags "-s -w -X main.version=$VERSION" -o /out/agents/dozor-agent-linux-$a ./cmd/dozor-agent; \
    done
# сервер с SQLite (cgo), статически
RUN CGO_ENABLED=1 go build -mod=vendor -trimpath \
      -ldflags "-s -w -X main.version=$VERSION -extldflags '-static'" -tags 'sqlite_omit_load_extension' \
      -o /out/dozor-server ./cmd/dozor-server

# --- итоговый образ ---
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -H -u 10001 dozor \
    && mkdir -p /var/lib/dozor && chown dozor /var/lib/dozor
COPY --from=go /out/dozor-server /usr/local/bin/dozor-server
COPY --from=go /out/agents /opt/dozor/agents
USER dozor
ENV DOZOR_LISTEN=:8443 DOZOR_DATA=/var/lib/dozor DOZOR_AGENTS_DIR=/opt/dozor/agents DOZOR_VM_URL=http://victoriametrics:8428
EXPOSE 8443
VOLUME /var/lib/dozor
HEALTHCHECK --interval=30s --timeout=5s CMD wget -q --no-check-certificate -O /dev/null https://127.0.0.1:8443/api/health || exit 1
ENTRYPOINT ["/usr/local/bin/dozor-server"]
