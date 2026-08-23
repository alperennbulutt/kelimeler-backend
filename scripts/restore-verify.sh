#!/usr/bin/env bash
#
# Bir yedeğin GERÇEKTEN geri yüklenebildiğini doğrular.
#
#   ./scripts/restore-verify.sh [dump_dosyasi]
#
# Dump verilmezse en yeni yedek kullanılır. Geri yükleme, ADI ÇAKIŞMAYAN
# geçici bir veritabanına yapılır ve iş bitince o veritabanı silinir —
# production verisine dokunulmaz.
#
# "Yedeğim var" ile "yedeğim geri yüklenebiliyor" farklı şeylerdir;
# bu betik ikincisini kanıtlar.
set -euo pipefail

BACKUP_DIR="${BACKUP_DIR:-./backups}"

if [[ -z "${DATABASE_URL:-}" && -f .env ]]; then
  # shellcheck disable=SC1091
  set -a; source .env; set +a
fi

if [[ -z "${DATABASE_URL:-}" ]]; then
  echo "HATA: DATABASE_URL tanımlı değil." >&2
  exit 1
fi

DUMP="${1:-}"
if [[ -z "$DUMP" ]]; then
  DUMP="$(find "$BACKUP_DIR" -name 'kelimeler-*.dump' -type f 2>/dev/null | sort | tail -1)"
fi
[[ -n "$DUMP" && -f "$DUMP" ]] || { echo "HATA: doğrulanacak yedek bulunamadı." >&2; exit 1; }

echo "Doğrulanan yedek: $DUMP"

# DATABASE_URL'in veritabanı adını geçici bir adla değiştir.
TMP_DB="kelimeler_restore_test_$$"
BASE_URL="${DATABASE_URL%/*}"
QUERY=""
[[ "$DATABASE_URL" == *"?"* ]] && QUERY="?${DATABASE_URL#*\?}"
TMP_URL="$BASE_URL/$TMP_DB$QUERY"
ADMIN_URL="$BASE_URL/postgres$QUERY"

cleanup() {
  psql "$ADMIN_URL" -q -c "DROP DATABASE IF EXISTS $TMP_DB;" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "Geçici veritabanı oluşturuluyor: $TMP_DB"
psql "$ADMIN_URL" -q -c "DROP DATABASE IF EXISTS $TMP_DB;"
psql "$ADMIN_URL" -q -c "CREATE DATABASE $TMP_DB;"

echo "Geri yükleniyor..."
# Uzantılar ve sahiplik farkları uyarı üretebilir; --exit-on-error kullanmıyoruz
# ama sonucu aşağıda veriyle doğruluyoruz.
pg_restore --dbname="$TMP_URL" --no-owner --no-acl "$DUMP" 2>&1 | grep -v "^$" | tail -5 || true

echo ""
echo "--- Doğrulama ---"
FAIL=0
check_table() {
  local table="$1" min="$2"
  local n
  n="$(psql "$TMP_URL" -tAc "SELECT COUNT(*) FROM $table;" 2>/dev/null || echo "HATA")"
  if [[ "$n" == "HATA" ]]; then
    printf "  %-20s OKUNAMADI\n" "$table"
    FAIL=1
    return
  fi
  if [[ "$n" -lt "$min" ]]; then
    printf "  %-20s %8s satır  (en az %s bekleniyordu)\n" "$table" "$n" "$min"
    FAIL=1
    return
  fi
  printf "  %-20s %8s satır  ✓\n" "$table" "$n"
}

# dictionary_words her zaman dolu olmalı; kullanıcı tabloları boş olabilir.
check_table dictionary_words 1
check_table users 0
check_table auth_identities 0
check_table word_progresses 0
check_table daily_logins 0

echo ""
if [[ "$FAIL" -ne 0 ]]; then
  echo "SONUÇ: geri yükleme DOĞRULANAMADI." >&2
  exit 1
fi
echo "SONUÇ: yedek geri yüklenebiliyor ve veri içeriyor. ✓"
