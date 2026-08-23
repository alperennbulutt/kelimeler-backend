#!/usr/bin/env bash
#
# PostgreSQL yedeği alır. Çıktı: zaman damgalı, sıkıştırılmış custom-format dump.
#
#   ./scripts/backup.sh [hedef_dizin]
#
# DATABASE_URL ortam değişkenini kullanır (.env'den de okunur).
set -euo pipefail

BACKUP_DIR="${1:-${BACKUP_DIR:-./backups}}"
RETENTION_DAYS="${BACKUP_RETENTION_DAYS:-14}"

# .env varsa oku (DATABASE_URL için).
if [[ -z "${DATABASE_URL:-}" && -f .env ]]; then
  # shellcheck disable=SC1091
  set -a; source .env; set +a
fi

if [[ -z "${DATABASE_URL:-}" ]]; then
  echo "HATA: DATABASE_URL tanımlı değil." >&2
  exit 1
fi

command -v pg_dump >/dev/null || { echo "HATA: pg_dump bulunamadı." >&2; exit 1; }

mkdir -p "$BACKUP_DIR"
STAMP="$(date +%Y%m%d-%H%M%S)"
OUT="$BACKUP_DIR/kelimeler-$STAMP.dump"

echo "Yedek alınıyor → $OUT"

# -Fc: custom format (sıkıştırılmış, seçmeli geri yükleme yapılabilir)
# --no-owner/--no-acl: farklı bir kullanıcıya geri yüklemeyi kolaylaştırır
pg_dump "$DATABASE_URL" \
  --format=custom \
  --compress=9 \
  --no-owner \
  --no-acl \
  --file="$OUT"

SIZE="$(du -h "$OUT" | cut -f1)"
echo "Tamamlandı: $OUT ($SIZE)"

# Boş/bozuk dump sessizce birikmesin.
if ! pg_restore --list "$OUT" >/dev/null 2>&1; then
  echo "HATA: üretilen dump okunamıyor, bozuk olabilir." >&2
  exit 1
fi

TABLES="$(pg_restore --list "$OUT" | grep -c 'TABLE DATA' || true)"
echo "Dump doğrulandı: $TABLES tablo verisi içeriyor."
if [[ "$TABLES" -eq 0 ]]; then
  echo "UYARI: dump hiç tablo verisi içermiyor." >&2
  exit 1
fi

# Eski yedekleri temizle.
if [[ "$RETENTION_DAYS" -gt 0 ]]; then
  DELETED="$(find "$BACKUP_DIR" -name 'kelimeler-*.dump' -mtime +"$RETENTION_DAYS" -print -delete | wc -l | tr -d ' ')"
  [[ "$DELETED" -gt 0 ]] && echo "$DELETED eski yedek silindi (>$RETENTION_DAYS gün)."
fi

echo "$OUT"
