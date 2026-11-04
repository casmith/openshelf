SHELL := bash
export PATH := $(shell mise bin-paths 2>/dev/null | tr '\n' ':')$(PATH)

DEV_DB_URL ?= postgres://openshelf:openshelf@localhost:5433/openshelf?sslmode=disable

.PHONY: dev generate css templ sqlc check test lint vet build image db-up db-down db-reset seed migrate

dev: db-up
	@trap 'kill 0' EXIT; \
	tailwindcss -i internal/web/styles/app.css -o internal/web/static/css/app.css --watch=always & \
	go tool templ generate --watch --cmd "go run ./cmd/openshelf serve" & \
	wait

generate: sqlc templ css

sqlc:
	sqlc generate

templ:
	go tool templ generate

css: templ
	tailwindcss -i internal/web/styles/app.css -o internal/web/static/css/app.css --minify 2>/dev/null

check: generate
	@git add -N . && git diff --exit-code --stat -- '*_templ.go' 'internal/db/*.go' || (echo "generated files differ from the index; run 'make generate' and stage them" && exit 1)
	go vet ./...
	golangci-lint run ./...
	$(MAKE) test

test:
	go test ./...

lint:
	golangci-lint run ./...

vet:
	go vet ./...

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/openshelf ./cmd/openshelf

image:
	docker build -t openshelf:dev .

db-up:
	docker compose -f compose.dev.yaml up -d --wait

db-down:
	docker compose -f compose.dev.yaml down

db-reset:
	docker compose -f compose.dev.yaml down -v
	$(MAKE) db-up

migrate:
	go run ./cmd/openshelf migrate

seed:
	go run ./cmd/openshelf seed
