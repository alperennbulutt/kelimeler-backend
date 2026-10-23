.PHONY: run build import-dictionary db-up up down logs test test-integration vet check ci fmt-check tidy fmt sync-web backup backup-verify

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
# Entegrasyon testleri TEST_DATABASE_URL tanımlı değilse atlanır; CI=true iken
# atlanmaz, başarısız olur (bkz. internal/db/dbtest).
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

fmt-check:
	@test -z "$$(gofmt -l .)" || { echo "gofmt uygulanmamış dosyalar:"; gofmt -l .; exit 1; }

# GitHub Actions'taki (.github/workflows/ci.yml) adımların lokal karşılığı;
# entegrasyon testleri dahil. Önce `make db-up`.
ci: fmt-check vet
	go build ./...
	CI=true TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test -race -count=1 ./...

tidy:
	go mod tidy

fmt:
	gofmt -w .

# Herkese açık sayfaların (gizlilik politikası, hesap silme) kaynağı Flutter
# reposundaki docs/ klasörüdür; internal/web/ altındaki kopyalar binary'ye gömülür.
# Önce iki kaynağın da varlığına bakılır ki eksikte yarım kopya kalmasın.
WEB_DOCS_DIR ?= ../kutularla_kelimeler/docs
WEB_PAGES := privacy-policy.html account-deletion.html
sync-web:
	@for f in $(WEB_PAGES); do \
		if [ ! -f "$(WEB_DOCS_DIR)/$$f" ]; then \
			echo "sync-web: kaynak bulunamadı: $(WEB_DOCS_DIR)/$$f" >&2; \
			echo "sync-web: Flutter reposu bu deponun yanında değilse: make sync-web WEB_DOCS_DIR=/yol/kutularla_kelimeler/docs" >&2; \
			exit 1; \
		fi; \
	done
	@for f in $(WEB_PAGES); do \
		cp "$(WEB_DOCS_DIR)/$$f" "internal/web/$$f" || exit 1; \
		echo "sync-web: $(WEB_DOCS_DIR)/$$f -> internal/web/$$f"; \
	done

# --- Yedekleme (bkz. BACKUP.md) ---

# Yedek al; dump okunabilir değilse veya boşsa hata verir.
backup:
	./scripts/backup.sh

# En yeni yedeği geçici bir veritabanına geri yükleyip doğrular.
# Production verisine dokunmaz.
backup-verify:
	./scripts/restore-verify.sh
