#!/usr/bin/env bash
#
# Bir yedeğin GERÇEKTEN geri yüklenebildiğini doğrular.
#
#   ./scripts/restore-verify.sh [dump_dosyasi]
#
#   MIN_USERS=1200 ./scripts/restore-verify.sh ~/indirilenler/coolify-yedek.dmp
#
# Dump verilmezse BACKUP_DIR'deki en yeni yedek kullanılır. Coolify'nin
# zamanlanmış yedeği gibi pg_restore'un okuyabildiği (custom format) her dump
# verilebilir. Ayarlar:
#   MIN_USERS             users tablosunda beklenen en az satır (varsayılan 0).
#                         Production yedeğini sınarken güncel kullanıcı
#                         sayısına yakın bir değer verin; yoksa kullanıcı
#                         verisi olmayan bir dump da "doğrulandı" sayılır.
#   MIN_DICTIONARY_WORDS  dictionary_words için en az satır (varsayılan 1).
# Geri yükleme, ADI ÇAKIŞMAYAN
# geçici bir veritabanına yapılır ve iş bitince o veritabanı silinir —
# production verisine dokunulmaz.
#
# "Yedeğim var" ile "yedeğim geri yüklenebiliyor" farklı şeylerdir;
# bu betik ikincisini kanıtlar.
set -euo pipefail

BACKUP_DIR="${BACKUP_DIR:-./backups}"
MIN_USERS="${MIN_USERS:-0}"
MIN_DICTIONARY_WORDS="${MIN_DICTIONARY_WORDS:-1}"

for v in MIN_USERS MIN_DICTIONARY_WORDS; do
  [[ "${!v}" =~ ^[0-9]+$ ]] || { echo "HATA: $v sayı olmalı (verilen: ${!v})." >&2; exit 1; }
done

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

# Tüm uygulama tabloları geri yüklenmiş ve okunabilir olmalı. dictionary_words
# her zaman dolu olmalı; users için alt sınır MIN_USERS ile verilir.
check_table dictionary_words "$MIN_DICTIONARY_WORDS"
check_table users "$MIN_USERS"
check_table auth_identities 0
check_table refresh_tokens 0
check_table word_progresses 0
check_table daily_logins 0
check_table earned_badges 0

echo ""
if [[ "$FAIL" -ne 0 ]]; then
  echo "SONUÇ: geri yükleme DOĞRULANAMADI." >&2
  exit 1
fi
echo "SONUÇ: yedek geri yüklenebiliyor ve veri içeriyor. ✓"
