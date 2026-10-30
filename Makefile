VERSION ?= 0.1.0
LDFLAGS = -s -w -X main.version=$(VERSION)
export GOFLAGS = -mod=vendor

.PHONY: all web agents server run clean

all: web agents server

web:
	cd web && npm ci --no-audit --no-fund && npm run build

agents:
	for a in amd64 arm64; do CGO_ENABLED=0 GOOS=linux GOARCH=$$a go build -trimpath -ldflags "$(LDFLAGS)" -o bin/agents/sentra-agent-linux-$$a ./cmd/sentra-agent; done

server:
	CGO_ENABLED=1 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/sentra-server ./cmd/sentra-server

# локальный запуск для разработки (нужен VictoriaMetrics на 127.0.0.1:8428)
run: all
	SENTRA_LISTEN=:8443 SENTRA_DATA=./bin/data SENTRA_AGENTS_DIR=./bin/agents ./bin/sentra-server

clean:
	rm -rf bin web/dist/assets
