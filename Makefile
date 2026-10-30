BUILD_DIRECTORY ?= bin
PREFIX ?= $(HOME)/.local

.PHONY: build test test-browser frontend-build frontend-dev verify-portable vet security run test-cli test-install install

build:
	docker build --platform linux/amd64 --target artifact --output type=local,dest="$(BUILD_DIRECTORY)" .

test:
	docker build --platform linux/amd64 --target test .

test-browser: build
	DOCKET_BINARY="$(abspath $(BUILD_DIRECTORY))/docket" node --test tests/browser.test.mjs

frontend-build:
	npm --prefix frontend ci
	npm --prefix frontend run build

frontend-dev:
	npm --prefix frontend run dev

verify-portable:
	docker build --platform linux/amd64 --target portable-test .

vet: test

security:
	docker build --platform linux/amd64 --target security .

test-cli: build
	DOCKET_BINARY="$(abspath $(BUILD_DIRECTORY))/docket" node --test tests/cli.test.mjs

install: build
	install -Dm755 "$(BUILD_DIRECTORY)/docket" "$(PREFIX)/bin/docket"

run: build
	"$(BUILD_DIRECTORY)/docket" serve

test-install: build
	DOCKET_BINARY="$(abspath $(BUILD_DIRECTORY))/docket" node --test tests/install.test.mjs
