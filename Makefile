VERSION ?= 0.1.0
LDFLAGS = -s -w -X main.version=$(VERSION)
export GOFLAGS = -mod=vendor

.PHONY: all web agents server run clean

all: web agents server

web:
	cd web && npm ci --no-audit --no-fund && npm run build

agents:
	for a in amd64 arm64; do CGO_ENABLED=0 GOOS=linux GOARCH=$$a go build -trimpath -ldflags "$(LDFLAGS)" -o bin/agents/dozor-agent-linux-$$a ./cmd/dozor-agent; done

server:
	CGO_ENABLED=1 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/dozor-server ./cmd/dozor-server

# локальный запуск для разработки (нужен VictoriaMetrics на 127.0.0.1:8428)
run: all
	DOZOR_LISTEN=:8443 DOZOR_DATA=./bin/data DOZOR_AGENTS_DIR=./bin/agents ./bin/dozor-server

clean:
	rm -rf bin web/dist/assets
