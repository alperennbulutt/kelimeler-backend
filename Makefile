.PHONY: run build import-dictionary db-up up down logs test test-integration vet check tidy fmt backup backup-verify

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

# Birim testleri: harici bağımlılık yok, her yerde çalışır.
# Entegrasyon testleri TEST_DATABASE_URL tanımlı değilse atlanır.
test:
	go test ./...

# Entegrasyon testleri dahil: gerçek Postgres'e ihtiyaç duyar.
# Önce `make db-up` ile veritabanını ayağa kaldırın.
TEST_DATABASE_URL ?= postgres://postgres:postgres@localhost:5434/kelimeler?sslmode=disable
test-integration:
	TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test ./... -v

vet:
	go vet ./...

# Yayın öncesi tek komut: derleme + statik analiz + testler.
check: build vet test

tidy:
	go mod tidy

fmt:
	gofmt -w .

# --- Yedekleme (bkz. BACKUP.md) ---

# Yedek al; dump okunabilir değilse veya boşsa hata verir.
backup:
	./scripts/backup.sh

# En yeni yedeği geçici bir veritabanına geri yükleyip doğrular.
# Production verisine dokunmaz.
backup-verify:
	./scripts/restore-verify.sh
