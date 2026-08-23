# Yedekleme ve Geri Yükleme

Kullanıcıların öğrenme ilerlemesi yalnızca bu veritabanında yaşıyor; kaybı
geri alınamaz. Bu belge yedek almayı **ve yedeğin gerçekten işe yaradığını
doğrulamayı** anlatır.

> Doğrulanmamış yedek, yedek değildir. `make backup` düzenli çalışsa bile
> `make backup-verify` çalıştırılmadıkça geri yükleyebildiğiniz kanıtlanmış olmaz.

---

## Hızlı kullanım

```bash
cd backend

make backup           # yedek al ve dump'ın okunabilirliğini doğrula
make backup-verify    # en yeni yedeği geçici bir veritabanına geri yükleyip sına
```

Yedekler varsayılan olarak `backend/backups/` altına, sıkıştırılmış
custom-format (`pg_dump -Fc`) olarak yazılır ve 14 günden eskiler silinir.

Ayarlar:

| Değişken | Varsayılan | Açıklama |
|---|---|---|
| `BACKUP_DIR` | `./backups` | Yedeklerin yazılacağı dizin |
| `BACKUP_RETENTION_DAYS` | `14` | Bu günden eski yedekler silinir (`0` = silme) |
| `DATABASE_URL` | `.env`'den | Yedeklenecek veritabanı |

---

## Geri yükleme doğrulaması ne yapar

`restore-verify.sh` production verisine **dokunmaz**:

1. `DATABASE_URL`'deki veritabanı adını `kelimeler_restore_test_<pid>` ile değiştirir.
2. Bu geçici veritabanını oluşturur ve yedeği oraya geri yükler.
3. Tabloların satır sayılarını kontrol eder (`dictionary_words` boş olmamalı).
4. Sonuç ne olursa olsun geçici veritabanını siler (`trap ... EXIT`).

Çıkış kodu 0 ise yedek geri yüklenebilir durumdadır.

---

## Gerçek bir felaketten dönüş

```bash
# 1. Uygulamayı durdur (yazma trafiği kesilsin)
docker compose stop app

# 2. Hedef veritabanını boşalt ve yedekten yükle
psql "$DATABASE_URL" -c "DROP SCHEMA public CASCADE; CREATE SCHEMA public;"
pg_restore --dbname="$DATABASE_URL" --no-owner --no-acl backups/kelimeler-YYYYMMDD-HHMMSS.dump

# 3. Uygulamayı başlat
docker compose start app
```

`DROP SCHEMA` mevcut veriyi **kalıcı olarak** siler. Bu komutu çalıştırmadan
önce mevcut durumun bir yedeğini alın — bozuk bir veritabanı bile,
yanlış yedeği yüklediğinizi fark ettiğinizde işinize yarayabilir.

---

## Otomatik yedekleme (cron)

Sunucuda her gece 03:00'te yedek almak ve haftada bir doğrulamak için:

```cron
0 3 * * *  cd /opt/kelimeler/backend && ./scripts/backup.sh >> /var/log/kelimeler-backup.log 2>&1
0 4 * * 0  cd /opt/kelimeler/backend && ./scripts/restore-verify.sh >> /var/log/kelimeler-backup.log 2>&1
```

Betikler başarısızlıkta sıfırdan farklı çıkış kodu döner; cron'un hata
çıktısını size ulaştırdığından emin olun (`MAILTO=` veya bir izleme kancası).

### Yedekleri sunucu dışına taşıyın

Yedek, veritabanıyla aynı makinede duruyorsa disk arızasına karşı korumaz.
`backups/` dizinini bir nesne depolamaya (S3, R2, B2) veya başka bir makineye
kopyalayan bir adım ekleyin. Sunucu sağlayıcınızın anlık görüntüleri
(snapshot) bunun yerini tutmaz: mantıksal bir hatayı (yanlış silme,
hatalı migration) anlık görüntüden seçmeli olarak geri alamazsınız.

---

## Sınırlar

- Betikler `pg_dump`/`pg_restore`/`psql` gerektirir; sunucuda PostgreSQL
  istemci araçları kurulu olmalıdır (`postgresql-client`).
- Geçici veritabanı oluşturabilmek için `DATABASE_URL`'deki kullanıcının
  `CREATEDB` yetkisi olmalıdır. Yoksa `restore-verify.sh` çalışmaz;
  bu durumda doğrulamayı ayrı bir bakım makinesinde yapın.
