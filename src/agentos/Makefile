# AgentOS build entry points (owned by module M8).
GO ?= go
VERSION ?= 0.1.22
export PATH := /usr/local/go/bin:$(PATH)
export VERSION

.PHONY: build tray test vet e2e run dist installer archives assets ci wine-smoke clean
build:            ## build host binary into bin/
	CGO_ENABLED=0 $(GO) build -o bin/agentos ./cmd/agentos
tray:             ## Windows tray launcher into dist/
	scripts/build.sh tray
test:             ## unit + e2e tests
	$(GO) test -race ./...
vet:
	$(GO) vet ./...
	GOOS=windows $(GO) vet ./...
	GOOS=darwin GOARCH=arm64 $(GO) vet ./...
e2e:
	$(GO) test -count=1 -v ./test/e2e/
run: build        ## all-in-one server + local node on :7800, data in ./.dev
	./bin/agentos server --data ./.dev --admin-password admin123
dist:             ## 3 cross-built binaries + tray + installer + archives + SHA256SUMS into dist/
	scripts/build.sh all
installer:
	scripts/build.sh installer
archives:
	scripts/build.sh archives && scripts/build.sh checksums
assets:           ## redraw icon / installer bitmaps
	$(GO) run ./packaging/tools/wintool assets
ci:               ## what CI runs
	scripts/ci.sh
wine-smoke: dist  ## run the Windows binaries + silent installer under Wine
	scripts/wine-smoke.sh
clean:
	rm -rf bin dist .dev cmd/agentos/*.syso cmd/agentos-tray/*.syso
