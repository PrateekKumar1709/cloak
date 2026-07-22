.PHONY: build test lint run demo install clean release eval e2e menubar

VERSION ?= 1.0.0
LDFLAGS := -ldflags "-X main.version=$(VERSION)"

build:
	go build $(LDFLAGS) -o bin/cloak ./cmd/cloak

install:
	go install $(LDFLAGS) ./cmd/cloak

# Optional macOS menubar companion (self-contained module; needs network on first build).
menubar:
	cd menubar && go build -o ../bin/cloak-menubar .
	@echo "Built bin/cloak-menubar. Run it for a live protected/on-device count (systray; best supported on macOS)."

test:
	go test ./...

lint:
	go vet ./...

run: build
	./bin/cloak start --demo

demo: build
	./scripts/start-lemonade.sh
	./bin/cloak demo

eval:
	go run ./eval -fixtures eval/fixtures/dev_leaks.jsonl
	@echo "--- with Lemonade Tier-2 (server must be up) ---"
	go run ./eval -fixtures eval/fixtures/dev_leaks.jsonl -tier2 || true

e2e:
	bash scripts/e2e_validate.sh
	bash scripts/e2e_lemonade.sh
	bash scripts/e2e_omni.sh

clean:
	rm -rf bin/

release:
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o bin/cloak-darwin-arm64 ./cmd/cloak
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o bin/cloak-darwin-amd64 ./cmd/cloak
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o bin/cloak-linux-amd64 ./cmd/cloak
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o bin/cloak-windows-amd64.exe ./cmd/cloak
