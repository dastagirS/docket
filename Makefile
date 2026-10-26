.PHONY: build test test-browser frontend-build frontend-dev verify-portable vet security run

build:
	docker build --platform linux/amd64 --target artifact --output type=local,dest=bin .

test:
	docker build --platform linux/amd64 --target test .

test-browser: build
	node --test tests/browser.test.mjs

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

run: build
	./bin/docket
