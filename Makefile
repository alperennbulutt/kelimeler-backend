.PHONY: run build import-dictionary db-up up down logs test tidy fmt

# Lokal geliştirme: sadece Postgres'i docker'da çalıştır, server'ı go run ile çalıştır.
db-up:
	docker compose up -d db

run:
	go run ./cmd/server

build:
	go build -o bin/server ./cmd/server
	go build -o bin/import ./cmd/import-dictionary

import-dictionary:
	go run ./cmd/import-dictionary

# Tüm yığını (postgres + app) docker'da çalıştır.
up:
	docker compose up --build

down:
	docker compose down

logs:
	docker compose logs -f

test:
	go test ./...

tidy:
	go mod tidy

fmt:
	gofmt -w .
