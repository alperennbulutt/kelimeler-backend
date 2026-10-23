# Yedekleme ve Geri Yükleme

Kullanıcıların öğrenme ilerlemesi yalnızca bu veritabanında yaşıyor; kaybı
geri alınamaz. Bu belge yedek almayı **ve yedeğin gerçekten işe yaradığını
doğrulamayı** anlatır.

> Doğrulanmamış yedek, yedek değildir. Yedek düzenli alınsa bile geri yükleme
> provası yapılmadıkça geri yükleyebildiğiniz kanıtlanmış olmaz.

---

## Hangi yöntem nerede geçerli

| Kurulum | Yedeği kim alır | Nereye | Geri yükleme provası |
|---|---|---|---|
| **Production** — Coolify Application + Coolify'nin yönettiği PostgreSQL (bkz. DEPLOYMENT.md) | Coolify'nin **Scheduled Backups** özelliği | Sunucu **dışında** S3 uyumlu depolama (Cloudflare R2, Backblaze B2, AWS S3) | Ayda bir, indirilen dump ile `scripts/restore-verify.sh`, ayrı bir makinede |
| **docker-compose** — lokal geliştirme ya da Coolify'siz, compose ile çalışan bir sunucu | `scripts/backup.sh` (`make backup`), cron ile | `BACKUP_DIR` (varsayılan `./backups`) + kendi kopyalama adımınız | `scripts/restore-verify.sh` (`make backup-verify`) |

Production'da `scripts/backup.sh` **kullanılmaz**: Coolify'nin PostgreSQL'i
yalnızca docker iç ağında çözülen bir adreste (`DATABASE_URL`'deki
`<kaynak-id>:5432`) dinler, host'taki bir cron ona bağlanamaz ve sunucuda
`postgresql-client` kurulu değildir. `scripts/restore-verify.sh` ise her iki
kurulumda da doğrulama aracıdır; yalnızca bir dump dosyası ve geçici
veritabanı açabileceği bir PostgreSQL ister.

---

## Production: Coolify zamanlanmış yedek

### 1. Sunucu dışı depolamayı tanımla (bir kez)

Yedek veritabanıyla aynı diskte durursa disk arızasına, sunucunun silinmesine
ya da ele geçirilmesine karşı korumaz.

1. S3 uyumlu bir bucket aç (ör. Cloudflare R2 `kelimeler-db-yedek`). Yalnızca
   bu bucket'a yazma/okuma yetkisi olan ayrı bir erişim anahtarı üret.
2. Bucket'ta bir **yaşam döngüsü (lifecycle) kuralı** tanımla: 90 günden eski
   nesneler silinsin. Coolify'nin kendi saklama ayarı çalışmasa bile bucket
   sonsuza dek büyümez.
3. Coolify → **Storages → + Add** (S3 Storage): endpoint (R2 için
   `https://<hesap-id>.r2.cloudflarestorage.com`), bucket, region (R2 için
   `auto`), access key, secret key. **Test Connection** başarılı olmalı.

### 2. Zamanlanmış yedeği aç (bir kez)

Coolify → Proje → **PostgreSQL kaynağı → Backups → + Add**:

| Ayar | Değer |
|---|---|
| Frequency | `0 3 * * *` (her gece 03:00, sunucu saat dilimiyle; trafiğin en düşük olduğu saat) |
| Databases | Uygulamanın veritabanı (`DATABASE_URL`'in sonundaki ad, ör. `postgres`) |
| Save to S3 | Açık, 1. adımdaki depolama seçili |
| Saklama (yerel) | Son 7 yedek / 7 gün |
| Saklama (S3) | 30 gün (bucket kuralı 90 günde ikinci emniyet) |

Ardından:

- **Backup Now** ile ilk yedeği elle al. Yedek satırı "Success" olmalı ve
  dosya bucket'ta görünmeli (boyutu birkaç yüz KB'dan büyük; sözlük tek başına
  ~10 bin satır).
- Coolify → **Notifications**'ta (e-posta, Discord ya da Telegram) yedek
  başarısızlığı bildirimini aç. Sessizce duran bir yedek, hiç olmayan yedekle
  aynıdır.

### 3. Aylık geri yükleme provası

Ayda bir (ve her büyük şema değişikliğinden sonra), production'a **dokunmadan**:

```bash
# 1. En yeni yedeği bucket'tan indir (Coolify → Backups → indir, ya da rclone/aws cli).
# 2. Ayrı bir makinede atılabilir bir PostgreSQL 16 aç (bu depoda: make db-up).
# 3. Dump'ı geçici bir veritabanına geri yükleyip doğrula:
DATABASE_URL="postgres://postgres:postgres@localhost:5434/postgres?sslmode=disable" \
MIN_USERS=<güncel kullanıcı sayısının ~%90'ı> \
  ./scripts/restore-verify.sh ~/indirilenler/<coolify-yedeği>.dmp
```

Güncel kullanıcı sayısı: Coolify → PostgreSQL kaynağı → Terminal →
`psql -U postgres -d <veritabanı-adı> -c "SELECT count(*) FROM users;"`.

`restore-verify.sh` dump'ı `kelimeler_restore_test_<pid>` adlı geçici bir
veritabanına yükler, tüm uygulama tablolarının okunabildiğini ve
`dictionary_words` ile `users` tablolarının alt sınırları geçtiğini denetler,
sonunda geçici veritabanını siler. Çıkış kodu 0 değilse yedek **güvenilir
değildir**: sebebini bul, düzelt, yeni bir yedekle provayı tekrarla.

Notlar:

- Coolify PostgreSQL yedeğini `pg_dump` custom formatında alır; dosyayı
  `pg_restore --list <dosya>` ile okuyabildiğini önce kontrol et. Düz SQL ya
  da sıkıştırılmış (`.gz`) bir dosya çıkarsa önce açıp `psql -f` ile geri yükle.
- Prova makinesindeki `pg_restore` sürümü sunucunun sürümünden (16) eski olmamalı.
- Her provayı kaydet (tarih, dump dosyası, `users` sayısı, sonuç) — ör. bu
  depodaki bir issue'ya ya da ekip notlarına. Kayıt yoksa prova yapılmamış sayılır.

### 4. Gerçek bir felaketten dönüş (production)

`DROP`/`--clean` mevcut veriyi **kalıcı olarak** siler. Başlamadan önce
mevcut durumun yedeğini al (**Backup Now**): bozuk bir veritabanı bile,
yanlış yedeği yüklediğini fark ettiğinde işine yarar.

1. **Yazma trafiğini kes:** Coolify → Application → **Stop**.
2. **Mevcut durumu yedekle:** PostgreSQL kaynağı → Backups → **Backup Now**.
3. **Dump'ı veritabanı container'ına kopyala ve geri yükle** (sunucuya SSH ile):

   ```bash
   # PostgreSQL container'ının adı (Coolify kaynak id'sini içerir):
   docker ps --format '{{.Names}}' | grep -i <postgres-kaynak-id>
   docker cp <yedek>.dmp <db-container>:/tmp/restore.dmp
   docker exec -it <db-container> pg_restore -U postgres -d <veritabanı-adı> \
     --clean --if-exists --no-owner --no-acl /tmp/restore.dmp
   docker exec <db-container> rm /tmp/restore.dmp
   ```

   Coolify sürümünde PostgreSQL kaynağının **Import Backup** ekranı varsa aynı
   işlemi oradan dosya yükleyerek de yapabilirsin.
4. **Uygulamayı başlat:** Application → **Start**. Açılış logunda
   `db: migrate tamamlandı` görünmeli; `https://<domain>/api/v1/health`
   `200` ve `"dictionaryWords":9980` dönmeli.
5. Kullanıcı sayısını ve rastgele bir kullanıcının ilerlemesini kontrol et
   (`SELECT count(*) FROM users;`, `SELECT count(*) FROM word_progresses;`).

---

## docker-compose kurulumu: `scripts/backup.sh`

Bu bölüm yalnızca veritabanı **compose ile** çalıştığında geçerlidir (lokal
geliştirme ya da Coolify'siz bir sunucu). Komutlar deponun kökünden çalışır.

```bash
make backup           # yedek al ve dump'ın okunabilirliğini doğrula
make backup-verify    # en yeni yedeği geçici bir veritabanına geri yükleyip sına
```

Yedekler varsayılan olarak `./backups/` altına, sıkıştırılmış custom-format
(`pg_dump -Fc`) olarak yazılır ve 14 günden eskiler silinir.

| Değişken | Varsayılan | Açıklama |
|---|---|---|
| `BACKUP_DIR` | `./backups` | Yedeklerin yazılacağı dizin |
| `BACKUP_RETENTION_DAYS` | `14` | Bu günden eski yedekler silinir (`0` = silme) |
| `DATABASE_URL` | `.env`'den | Yedeklenecek veritabanı (host'tan erişilebilir olmalı; compose'da `localhost:5434`) |
| `MIN_USERS` | `0` | `restore-verify.sh`: `users` için beklenen en az satır |
| `MIN_DICTIONARY_WORDS` | `1` | `restore-verify.sh`: `dictionary_words` için beklenen en az satır |

### Otomatik yedekleme (cron)

Sunucuda her gece 03:00'te yedek almak ve haftada bir doğrulamak için
(`/yol/kelimeler-backend` deponun sunucudaki yolu):

```cron
0 3 * * *  cd /yol/kelimeler-backend && ./scripts/backup.sh >> /var/log/kelimeler-backup.log 2>&1
0 4 * * 0  cd /yol/kelimeler-backend && MIN_USERS=1 ./scripts/restore-verify.sh >> /var/log/kelimeler-backup.log 2>&1
```

Betikler başarısızlıkta sıfırdan farklı çıkış kodu döner; cron'un hata
çıktısını size ulaştırdığından emin olun (`MAILTO=` veya bir izleme kancası).

`backups/` dizinini bir nesne depolamaya (S3, R2, B2) ya da başka bir makineye
kopyalayan bir adım ekleyin (ör. `rclone copy backups/ r2:kelimeler-db-yedek`).
Sunucu sağlayıcınızın anlık görüntüleri (snapshot) bunun yerini tutmaz:
mantıksal bir hatayı (yanlış silme, hatalı migration) anlık görüntüden seçmeli
olarak geri alamazsınız.

### Geri yükleme (compose)

```bash
# 1. Uygulamayı durdur (yazma trafiği kesilsin)
docker compose stop app

# 2. Hedef veritabanını boşalt ve yedekten yükle
psql "$DATABASE_URL" -c "DROP SCHEMA public CASCADE; CREATE SCHEMA public;"
pg_restore --dbname="$DATABASE_URL" --no-owner --no-acl backups/kelimeler-YYYYMMDD-HHMMSS.dump

# 3. Uygulamayı başlat
docker compose start app
```

---

## Sınırlar

- Betikler `pg_dump`/`pg_restore`/`psql` gerektirir; çalıştıkları makinede
  PostgreSQL istemci araçları (16 ya da daha yeni) kurulu olmalıdır.
- `restore-verify.sh` geçici veritabanı oluşturabilmek için `DATABASE_URL`'deki
  kullanıcının `CREATEDB` yetkisine ihtiyaç duyar ve yönetim bağlantısı için
  aynı sunucudaki `postgres` veritabanını kullanır. Bu yüzden provayı
  production veritabanına karşı değil, ayrı bir makinedeki atılabilir bir
  PostgreSQL'e karşı yapın.
