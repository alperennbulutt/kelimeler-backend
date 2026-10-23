# Production Deploy — Coolify (GitHub + otomatik domain)

Bu rehber backend'i Coolify ile sunucuya (45.87.120.58) deploy eder.
Mimari: **Coolify Application (Go container)** + **Coolify managed PostgreSQL** + **Traefik (otomatik SSL'li domain)**.

> Port derdi yok: Coolify her uygulamayı Traefik reverse-proxy arkasında domain bazlı yönlendirir; container içinde 8080 dinlemen yeterli.

---

## 1) Depo

Backend kendi GitHub deposundadır (`alperennbulutt/kelimeler-backend`, dal `main`); Flutter uygulaması ayrı bir depodadır. `.gitignore` sayesinde `secrets/` ve `.env` push **edilmez** (yalnızca `.env.production.example` ve `seed/` gider).

```bash
git add -A        # internal/web/*.html go:embed hedefidir; commit'e girmezse derleme kırılır
git commit -m "..."
git push origin main
```

Depo özelse Coolify'de kaynak olarak **GitHub App** (Private Repository) kullanılmalıdır. "Public Repository" kaynağı özel depoyu klonlayamaz; deploy `fatal: could not read Username for 'https://github.com'` hatasıyla düşer.

---

## 2) Coolify'de PostgreSQL oluştur

- Coolify panel → Projeni seç → **+ New Resource → Database → PostgreSQL** (16)
- Oluşunca **"Postgres URL (internal)"** değerini kopyala — Application'ın `DATABASE_URL`'i bu olacak.
  Örnek: `postgres://postgres:xxxx@abc123:5432/postgres`

- **Yedekleme:** Kaynağı oluşturduktan hemen sonra, ilk kullanıcı gelmeden **Backups** bölümünden sunucu dışına (S3 uyumlu depolama) günlük zamanlanmış yedeği aç ve bir geri yükleme provası yap. Adımlar, saklama ayarları ve felaketten dönüş: [BACKUP.md](BACKUP.md). Depodaki `scripts/backup.sh` bu kurulum için **değildir** (Coolify DB'si host'tan erişilemez); yalnızca docker-compose düzeninde ve geri yükleme provasında kullanılır.

---

## 3) Coolify'de Application oluştur

- **+ New Resource → Application → Private/Public Repository** → GitHub reposunu seç
- **Build Pack:** `Dockerfile` (son aşama çalışma imajıdır; hedef/target girme)
- **Build → "Include Source Commit in Build":** açık. Coolify commit'i `SOURCE_COMMIT` build arg'ı olarak verir, binary'ye sürüm olarak gömülür (açılış logunda `kelimeler-backend sürüm: <commit>`) ve `SENTRY_RELEASE` boşsa Sentry sürümü olur. Kapalıysa sürüm `bilinmiyor` görünür.
- **Port (Ports Exposes):** `8080`
- **Health Check:** aşağıdaki nota bak.

> **Sağlık kontrolü ve distroless imaj:** Coolify'nin arayüzden açılan health check'i probe'u container **içinde** `curl`/`wget` ile çalıştırır. Çalışma imajı distroless'tır (kabuk, `curl`, `wget` yok); bu yüzden o probe hiç çalışamaz ve container hep "unhealthy" görünür, deploy geri alınabilir. Bunun yerine imajın kendi `HEALTHCHECK`'i kullanılır: `Dockerfile`'daki `HEALTHCHECK CMD ["/server", "-healthcheck"]` binary'nin kendisiyle `http://127.0.0.1:$PORT/api/v1/health`'e istek atar ve yalnızca 200'de başarılı sayılır.
>
> - Coolify → Application → **Health Check** bölümünde arayüz health check'ini **kapalı** tut; Coolify imajda `HEALTHCHECK` tanımlıysa onu kullanır. (Coolify sürümüne göre bu davranışı ilk deploy'da doğrula.)
> - Doğrulama: sunucuda `docker ps` çıktısında container durumu `(healthy)` olmalı; ya da `docker inspect --format '{{.State.Health.Status}}' <container>` → `healthy`. `unhealthy` ise `docker inspect --format '{{json .State.Health.Log}}' <container>` probe çıktısını gösterir.
> - `/api/v1/health` artık veritabanını 2 sn'lik zaman aşımıyla sınar: DB'ye ulaşılamazsa `503 {"status":"unavailable","code":"db_unavailable"}` döner. Başarılı yanıt değişmedi: `{"dictionaryWords":9980,"status":"ok"}`.

---

## 4) Environment Variables (Application → Environment)

`.env.production.example` referansıyla şunları gir:

| Anahtar | Değer |
|---|---|
| `APP_ENV` | `production` — **tam olarak bu**, küçük harfle. Boş ya da başka bir değerde (`prod`, `Production`) sunucu açılmaz |
| `PORT` | `8080` |
| `DATABASE_URL` | Adım 2'deki internal URL (DB adı URL'in sonunda; ör. `.../postgres`) |
| `DB_MAX_OPEN_CONNS` | `25` — açık bağlantı üst sınırı. Dolunca yeni istekler bekler; PostgreSQL'in `max_connections` değerini (varsayılan 100) aşmasın |
| `DB_MAX_IDLE_CONNS` | `10` — havuzda boşta tutulan bağlantı sayısı |
| `DB_CONN_MAX_LIFETIME` | `1h` — bir bağlantının yeniden açılmadan kullanılacağı en uzun süre |
| `DB_STATEMENT_TIMEOUT` | `10s` — her sorguya PostgreSQL'de uygulanan üst süre (`statement_timeout`). Kaçak ya da kilitte bekleyen sorgu iptal edilir, istek 500 alır, bağlantı havuza döner. `0` kapatır. Migration ve sözlük seed'i bu sınırdan muaftır; `DATABASE_URL`'de `statement_timeout` verilmişse o geçerlidir. Sürecin ya da oturumun saat dilimine (`TimeZone`) dokunulmaz |
| `JWT_SECRET` | `openssl rand -base64 48` çıktısı (güçlü, gizli). 32 bayttan kısa ya da `.env*.example`'daki yer tutucu girilirse sunucu açılmaz |
| `ACCESS_TOKEN_TTL` | `15m` |
| `REFRESH_TOKEN_TTL` | `720h` |
| `GOOGLE_CLIENT_IDS` | `928054180858-...apps.googleusercontent.com` |
| `APPLE_CLIENT_ID` | iOS bundle id'n (Apple yoksa boş) |
| `APPLE_TEAM_ID` | Apple Developer hesabındaki 10 karakterlik Team ID — **iOS yayını için zorunlu** (aşağıdaki nota bak) |
| `APPLE_KEY_ID` | "Sign in with Apple" için üretilen anahtarın Key ID'si — **iOS yayını için zorunlu** |
| `APPLE_PRIVATE_KEY` | O anahtarın `.p8` içeriği (satır sonları `\n` olarak kaçırılabilir) — **iOS yayını için zorunlu**, gizli |
| `USAGE_TIMEZONE` | `Europe/Istanbul` — günlük kullanım sayaçları bu saat diliminin gece yarısında sıfırlanır (girilmezse varsayılan da budur) |
| `GATE_ENFORCEMENT` | `log` (varsayılan). Reklam kapısı ihlallerinde sunucunun tepkisi: `off` denetlemez, `log` ihlali loglayıp Sentry'ye bilgi olarak gönderir ama isteği kabul eder, `enforce` isteği `403 gate_required` / `review_gate_required` ile reddeder. `enforce`'a ancak istemcinin bu kodları işleyen sürümü yaygınlaştıktan ve `log` kipinde ihlal kalmadığı görüldükten sonra geç. Başka bir değerde sunucu açılmaz |
| `API_RATE_LIMIT` | `600` — genel API hız sınırı, istek/dk. Geçerli access token'lı istekler kullanıcı başına, diğerleri IP başına sayılır. Kimliksiz uçlarda aynı IP'yi paylaşan (CGNAT) kullanıcılar sınırı paylaştığı için düşük tutma |
| `TRUSTED_PROXIES` | **Boş bırak** (varsayılan: özel ağlar + loopback). İstemci IP başlığına yalnızca bu adreslerden gelen bağlantıda güvenilir. Daraltmak istersen Coolify ağının CIDR'ını gir (aşağıya bak) |
| `PROXY_HEADER` | `X-Forwarded-For` (varsayılan). Cloudflare'e geçilirse aşağıdaki nota bak |
| `AUTH_RATE_LIMIT` | `10` — Google/Apple giriş uçlarının sıkı hız sınırı, IP başına istek/dk. `/auth/refresh` buna değil `API_RATE_LIMIT`'e tabidir |
| `SENTRY_DSN` | Sentry projesinin DSN'i. Boşsa hata raporlama kapalıdır |
| `SENTRY_RELEASE` | Boş bırakılabilir: imaj derlenirken gömülen sürüm (commit) kullanılır. Elle verilirse o geçerlidir |
| `CORS_ALLOWED_ORIGINS` | **Boş bırak** (API'yi çağıran bir web sitesi yoksa). Gerekirse virgülle ayrılmış origin listesi, ör. `https://panel.example.com` |

> `USAGE_TIMEZONE` yalnızca günlük kullanım sayaçlarını etkiler. Sürecin genel saat dilimini değiştiren `TZ` değişkenini **girme**: giriş serisi ve tekrar listesi sunucunun yerel gününe göre hesaplanır, `TZ` bunların gün sınırını da kaydırır.

> **Apple revoke anahtarları:** `APPLE_TEAM_ID`, `APPLE_KEY_ID` ve `APPLE_PRIVATE_KEY` üçü birden girilmezse hesap silinirken Apple'daki uygulama yetkisi iptal **edilmez** (silme yine çalışır, açılış loguna uyarı düşer). Apple bu iptali şart koşar ve hesap silme sayfası bunu vaat eder. İptal için gereken Apple refresh token'ı yalnızca anahtarlar girildikten **sonra** yapılan Apple girişlerinde saklanır; anahtarları ilk yayından önce gir.

> **Hız sınırları:** `API_RATE_LIMIT` ve `AUTH_RATE_LIMIT` için `0` ya da negatif değer sınırı kapatmaz; uyarı loglanır ve varsayılana (600 / 10) dönülür.

### İstemci IP'si, Traefik ve hız sınırı

Hız sınırları istemci IP'sine (kimliği doğrulanmış genel uçlarda kullanıcıya) göre sayılır. IP, `X-Forwarded-For` başlığından **yalnızca** bağlantı `TRUSTED_PROXIES` içindeki bir adresten geliyorsa okunur; başlıkta birden çok adres varsa ilk geçerli IP alınır. Doğrudan internetten gelen bir bağlantının başlığı yok sayılır, bağlantının karşı ucu kullanılır.

Bu kurgu Coolify'nin Traefik'inin varsayılan davranışına dayanır: Traefik, güvenmediği kaynaktan (internetteki istemci) gelen `X-Forwarded-*` başlıklarını siler ve `X-Forwarded-For`'u istemcinin gerçek adresiyle yeniden yazar. Bunun için:

- Coolify → **Servers → Proxy** yapılandırmasında `--entrypoints.https.forwardedHeaders.insecure=true` **olmamalı**; `forwardedHeaders.trustedIPs` de yalnızca gerçekten önünde duran bir proxy (ör. Cloudflare) varsa ve onun aralıklarıyla doldurulmalı.
- Uygulama container'ının 8080 portu host'a **yayınlanmamalı** (Coolify'de "Ports Mappings" boş, yalnızca "Ports Exposes: 8080"). Yayınlanırsa docker ağ geçidinden (özel adres) gelen bağlantı güvenilir sayılır.
- `TRUSTED_PROXIES`'i daraltmak istersen Traefik'in bağlandığı ağın CIDR'ını kullan: sunucuda `docker network inspect coolify -f '{{range .IPAM.Config}}{{.Subnet}}{{end}}'`.

Doğrulama (deploy sonrası, uydurma başlıkla): 11. istekte `429` gelmeli. Gelmiyorsa Traefik istemci başlığını koruyordur.

```bash
for i in $(seq 11); do
  curl -s -o /dev/null -w '%{http_code}\n' -X POST https://<domain>/api/v1/auth/google \
    -H "X-Forwarded-For: 10.9.8.$i" -H 'Content-Type: application/json' -d '{"idToken":"x"}'
done
```

**Cloudflare'e geçilirse:** Traefik Cloudflare'e güvenmediği sürece `X-Forwarded-For` yalnızca Cloudflare kenar IP'sini taşır ve tüm kullanıcılar aynı kovaya düşer. İki yol var: (1) Traefik'te `forwardedHeaders.trustedIPs`'e Cloudflare aralıklarını girmek (önerilen; `X-Forwarded-For` başlığı istemci IP'siyle gelir), ya da (2) `PROXY_HEADER=CF-Connecting-IP` yapmak; bu durumda sunucuya **yalnızca** Cloudflare üzerinden ulaşılabildiğinden emin ol (firewall'da 80/443'ü Cloudflare aralıklarına kısıtla), yoksa başlık uydurulabilir.

### Açılış denetimi

Sunucu açılırken yapılandırmayı denetler. Şu durumlarda **açılmaz** (deploy logunda `config: ...` satırı ve sebebi görünür; Coolify eski container'ı çalıştırmaya devam eder):

- `APP_ENV` tanımsız ya da `production`/`development` dışında bir değer.
- Production'da `JWT_SECRET` 32 bayttan kısa, kod varsayılanı ya da örnek dosyalardaki yer tutuculardan biri.
- Production'da sözlük tablosu boş ve imajdaki seed yüklenemiyor (logda `sözlük yüklenemedi`).

Şunlar açılışı engellemez ama production'da açılış loguna `config: uyarı:` satırı düşer ve Sentry'ye uyarı olarak gider:

- `APPLE_TEAM_ID`/`APPLE_KEY_ID`/`APPLE_PRIVATE_KEY`'den biri eksik (hesap silmede Apple token iptali yapılmaz) ya da anahtar geçersiz.
- `GOOGLE_CLIENT_IDS` boş (her Google girişi reddedilir).
- `TEST_MODE_UNLOCK_ALL=true` (tüm gruplar kilitsiz açılır, reklam kapıları devre dışı).

### Production'a özgü davranışlar

`APP_ENV=production` iken sunucu bilerek farklı davranır:

- **Parola ile kayıt/giriş kapalıdır.** `POST /api/v1/auth/register` ve `/auth/login` hiç mount edilmez (404 döner). Mobil istemci yalnızca Google/Apple girişi sunar ve parola kaydında e-posta sahipliği doğrulanmaz; uç açık kalırsa herkes başkasının adresiyle hesap açabilir. Lokal geliştirme ve E2E koşuları `APP_ENV=development` ile çalışır, orada iki uç da açıktır.
- **CORS:** `CORS_ALLOWED_ORIGINS` boşsa CORS middleware'i hiç eklenmez; yanıtlara `Access-Control-*` başlığı yazılmaz ve tarayıcılar başka bir siteden gelen isteğin yanıtını okutmaz. Mobil istemci CORS'tan etkilenmez. (Geliştirmede liste boşken tüm origin'lere izin verilir.)
- **Sayaç sıfırlama ucu yoktur:** `POST /api/v1/users/me/usage/reset` yalnızca production dışında açıktır.

Sosyal girişte e-postayla hesap eşleme her ortamda aynıdır: Google/Apple'ın **doğrulamadığı** e-posta kullanılmaz, ve e-postası eşleşen hesabın parola kimliği varsa bağlama yapılmaz (`409 email_in_use`).

**Yayın öncesi tek seferlik denetim:** canlı sunucu parola uçları açıkken çalıştıysa veritabanında parola ile açılmış hesaplar kalmış olabilir. İlk mağaza yayınından önce kalmadığını doğrula (Coolify → PostgreSQL kaynağı → Terminal):

```sql
SELECT u.id, u.email, u.created_at
FROM users u JOIN auth_identities ai ON ai.user_id = u.id
WHERE ai.provider = 'password';
```

Mobil istemci parola ile giriş sunmadığı için bu satırlar gerçek bir kullanıcıya ait olamaz (geliştirme/test artığı ya da başkasının adresiyle yapılmış kayıt). İnceleyip sil (`DELETE FROM users WHERE id IN (...)`, ilişkili veriler CASCADE ile temizlenir). Satır kalırsa o adresin sahibi Google/Apple ile giremez (409 alır).

---

## 5) Domain + Deploy

- Application → **Generate Domain** (Coolify otomatik `https://...sslip.io` domaini + Let's Encrypt SSL verir)
- **Deploy** → Coolify Dockerfile'ı build edip başlatır.
- Test: tarayıcıda `https://<domain>/api/v1/health` → `{"dictionaryWords":9980,"status":"ok"}`

### Yayın sonrası doğrulama

`git push` tek başına yeni sürümün canlıda olduğunu göstermez; her deploy'dan sonra şunları kontrol et:

1. **Deploy logu:** `db: migrate tamamlandı` satırı var (yeni kolonlar açılışta eklenir; migrate başarısızsa süreç hiç ayağa kalkmaz). Hemen üstünde `kullanım günü saat dilimi: Europe/Istanbul` görünmeli.
2. **`GET /api/v1/users/me/usage`** (geçerli bir access token ile) yanıtında şu alanlar var: `reviewAnswersToday`, `unlockedGroupsToday`, `noFillPassesRemaining`. Biri eksikse canlıda eski derleme çalışıyordur.
3. **Yanıt başlıkları:** herhangi bir `/api/v1/...` yanıtında `X-RateLimit-Limit`, `X-RateLimit-Remaining`, `X-RateLimit-Reset` geliyor (başlık adlarında büyük/küçük harf fark etmez) ve `X-RateLimit-Limit` değeri `API_RATE_LIMIT` ile aynı. Uydurma `X-Forwarded-For` ile hız sınırı atlatılamıyor (bkz. "İstemci IP'si, Traefik ve hız sınırı").
4. **Açılış logunda uyarı yok:** `ortam: APP_ENV=production` satırı görünmeli. `config: uyarı:` ile başlayan satırlar eksik bir ayarı (Apple revoke anahtarları, `GOOGLE_CLIENT_IDS`, `TEST_MODE_UNLOCK_ALL`) ya da geçersiz bir hız sınırı değerini gösterir.
5. **Parola uçları kapalı:** `POST /api/v1/auth/register` 404 dönüyor. 400 ya da 201 dönüyorsa `APP_ENV` `production` değildir.
6. **CORS kapalı** (`CORS_ALLOWED_ORIGINS` boşsa): `Origin` başlıklı isteğin yanıtında `Access-Control-Allow-Origin` yok.

```bash
# Başlıklar için token gerekmez:
curl -si https://<domain>/api/v1/health
# Kullanım alanları:
curl -s https://<domain>/api/v1/users/me/usage -H "Authorization: Bearer <access-token>"
# Parola uçları (404 beklenir):
curl -s -o /dev/null -w '%{http_code}\n' -X POST https://<domain>/api/v1/auth/register
# CORS (çıktı boş olmalı):
curl -si https://<domain>/api/v1/health -H 'Origin: https://ornek.example' | grep -i '^access-control-'
```

---

## 6) Kelime sözlüğü (otomatik yüklenir)

Elle bir şey çalıştırmak gerekmez. Sözlük imajın içindedir (`/seed/dictionary_words.sql`) ve uygulama her açılışta `dictionary_words` tablosu **boşsa** onu kendisi yükler; tablo doluysa dokunmaz.

Doğrula:

- İlk açılışın deploy logunda `db: seed tamamlandı, 9980 kelime yüklendi` satırı var.
- `https://<domain>/api/v1/health` yanıtında `"dictionaryWords":9980` görünüyor.

> Çalışma imajı distroless'tır ve root olmayan kullanıcıyla (`nonroot`, uid 65532) çalışır: içinde kabuk ve `psql` yoktur, Application terminali açılmaz. Firebase import aracı (`/import`) çalışma imajında yoktur; gerekirse lokalde `make import-dictionary` ya da `docker compose run --rm import` (ayrı `import` build hedefi) kullanılır. Veritabanına elle bakmak gerekirse Coolify'de **PostgreSQL kaynağının** terminalini kullan (`psql -U postgres -d <veritabanı-adı>`).

### Sözlüğü yeniden yüklemek

Kelimeler değiştiyse:

1. Yeni `seed/dictionary_words.sql` dosyasını üret (lokalde `make import-dictionary`, ardından `dictionary_words` tablosunun `pg_dump --data-only --column-inserts` dökümü), commit + deploy et.
2. PostgreSQL kaynağının terminalinde tabloyu boşalt: `psql -U postgres -d <veritabanı-adı> -c "TRUNCATE dictionary_words;"`
3. Application'ı yeniden başlat; boş tabloyu görünce yeni seed'i yükler.

2. ve 3. adım arasında uygulama kelime gösteremez; ikisini art arda ve sakin bir saatte yap. Tabloyu boşaltıp yeniden başlatmayı unutma: sözlük yalnızca açılışta yüklenir.

---

## 7) Flutter'ı production'a yönlendir

Uygulama build alırken backend adresini ver:

```bash
flutter build apk --release --dart-define=API_BASE_URL=https://<coolify-domain>/api/v1
# iOS:
flutter build ipa --release --dart-define=API_BASE_URL=https://<coolify-domain>/api/v1
```

---

## Herkese açık sayfalar (gizlilik politikası, hesap silme, app-ads.txt)

Mağaza kayıtlarının istediği herkese açık sayfalar, ayrı bir statik site kaynağı
yerine bu uygulamanın içinden servis edilir. Dosyalar `go:embed` ile binary'ye
gömülür; rotalar `internal/web/web.go` içindedir.

| Sayfa | Yollar | Kaynak |
|---|---|---|
| Gizlilik politikası | `/privacy`, `/privacy-policy`, `/privacy-policy.html`, `/gizlilik` | `internal/web/privacy-policy.html` |
| Hesap ve veri silme | `/account-deletion`, `/account-deletion.html`, `/hesap-silme` | `internal/web/account-deletion.html` |
| app-ads.txt | `/app-ads.txt` | `internal/web/web.go` içindeki `AppAdsTxt` sabiti |

Kök adres (`/`) `/privacy`'ye yönlenir. `.html` uzantılı yollar kaldırılmamalı:
sayfalar birbirine göreli bağlantı verir (`privacy-policy.html` ↔
`account-deletion.html`), yani `/privacy`'deki bağlantıya tıklayan tarayıcı
`/account-deletion.html` ister.

### Mağazalara girilecek adresler

- **Gizlilik politikası** (Play Console ve App Store Connect): `https://<domain>/privacy`
- **Hesap silme** (Play Console → Veri güvenliği formundaki hesap silme URL'si):
  `https://<domain>/account-deletion`. Google Play, hesap açılabilen uygulamalarda
  uygulamayı yüklemeden erişilebilen bu adresi şart koşar.

### app-ads.txt

AdMob bu dosyayı API adresinde değil, **mağaza kaydındaki geliştirici web
sitesinin** alan adının kökünde arar (`https://<alan-adı>/app-ads.txt`). Bu yüzden
rota ancak mağaza kaydına geliştirici web sitesi olarak **gerçek bir alan adı**
girildiğinde ve o alan adı bu uygulamaya yönlendirildiğinde işe yarar.

Coolify'nin ürettiği `...sslip.io` adresiyle AdMob doğrulaması **çalışmaz**:
AdMob'un tarama robotu adresi kök alan adına indirger ve o alan adı bize ait
değildir. Gerçek alan adı bağlanana kadar AdMob panelinde app-ads.txt durumunun
doğrulanmamış görünmesi beklenen durumdur.

Dosyanın içeriği tek satırdır; yayıncı kimliği uygulamanın AdMob kimliklerindeki
(`ca-app-pub-2597183942350435~...`) kimliktir:

```
google.com, pub-2597183942350435, DIRECT, f08c47fec0942fa0
```

### Metni güncelleme

Sayfaların kaynağı Flutter reposundaki `docs/` klasörüdür. `internal/web/`
altındaki HTML dosyaları onun bayt bayt kopyasıdır; **elle düzenleme**.

1. Flutter reposunda `docs/privacy-policy.html` / `docs/account-deletion.html`
   dosyasını düzenle; sayfanın başındaki "Son Güncelleme" tarihini de değiştir.
2. Bu depoda `make sync-web` çalıştır. Flutter reposunu `../kutularla_kelimeler`
   yolunda arar; başka yerdeyse `make sync-web WEB_DOCS_DIR=/yol/docs`.
3. `make check`, ardından commit + deploy. Sayfalar binary'ye gömülü olduğu için
   değişiklik ancak yeni deploy ile yayına girer.
4. Gizlilik politikası değiştiyse uygulama içi kopyayı da güncelle:
   `lib/features/legal/presentation/privacy_policy_page.dart`. Bu ayrı tutulan
   bir kopyadır, `sync-web` ona dokunmaz.

### Deploy sonrası kontrol

```bash
curl -s  https://<domain>/privacy | grep -c 'devruler.support@gmail.com'   # 1 veya daha fazla
curl -si https://<domain>/account-deletion.html | head -1                   # 200
curl -s  https://<domain>/app-ads.txt                                       # yukarıdaki satır
```

Sayfalar bir saat önbelleklenir (`Cache-Control: public, max-age=3600`);
tarayıcıda eski metni görüyorsan `curl` ile ya da gizli pencerede bak.

---

## Notlar

- **Düzgün kapanma:** Container SIGTERM aldığında sunucu yeni bağlantı kabul etmeyi bırakır, süren isteklerin bitmesini en fazla `SHUTDOWN_TIMEOUT` (varsayılan `8s`) bekler, ardından veritabanı havuzunu kapatıp Sentry kuyruğunu boşaltır (logda `sunucu düzgün kapandı`). Docker durdurma süresi varsayılan 10 sn'dir; bu süre dolarsa süreç SIGKILL ile öldürülür. `SHUTDOWN_TIMEOUT`'u artıracaksan container'ın durdurma süresini de (Coolify'de destekleniyorsa; docker-compose'da `stop_grace_period`) ondan en az birkaç saniye uzun tut.
- **Yedek:** Production yedeği Coolify'nin zamanlanmış yedeğidir (S3'e); ayda bir `scripts/restore-verify.sh` ile prova yapılır. Bkz. [BACKUP.md](BACKUP.md).
- **CI:** `.github/workflows/ci.yml` her `main` push'unda ve pull request'te gofmt, `go vet`, `go build`, testleri (PostgreSQL 16 servisiyle, entegrasyon testleri dahil, `-race`) ve `docker build`'i çalıştırır. CI'da `TEST_DATABASE_URL` boşsa entegrasyon testleri atlanmaz, başarısız olur. GitHub → Settings → Branches'ta `main` için bu iki işi (`test`, `docker`) zorunlu kontrol yap; Redeploy'u yalnızca yeşil commit için bas. Lokal karşılığı: `make db-up && make ci`.
- **Güncelleme:** `git push` sonrası Coolify'de **Redeploy**'a bas. Otomatik deploy (webhook) kurulu değildir; kurulana kadar push tek başına canlıyı güncellemez. Her deploy'dan sonra "Yayın sonrası doğrulama" listesini uygula.
- **Google/Apple Sign-In:** OAuth client'lar Google Cloud / Apple Developer tarafında. Backend yalnızca idToken **doğrular**; production domaini eklemek gerekmez ama Google Cloud Console'da OAuth ayarlarının geçerli olduğundan emin ol.
- **Güvenlik:** `JWT_SECRET`'i kimseyle paylaşma; sunucu root şifreni deploy sonrası değiştir.
- **Hata raporlama:** `SENTRY_DSN` girilirse panic'ler ve 5xx dönen tüm hatalar (ör. veritabanına ulaşılamaması, sorgu zaman aşımı) Sentry'ye gönderilir; girilmezse raporlama kapalıdır. Her iki durumda da istemci genel bir 500 mesajı alır, asıl hata konteyner loguna `hata: <status> <method> <path> request_id=<id>: <hata>` satırıyla, panic stack'i stderr'e yazılır. 4xx yanıtlar Sentry'ye gitmez.
- **İstek kimliği:** Her yanıtta `X-Request-ID` başlığı döner (istekte geçerli bir değer gelirse korunur, yoksa UUID üretilir). Aynı kimlik erişim logu satırında, `hata:` satırında ve Sentry olayının `request_id` etiketinde bulunur; kullanıcı bir hata bildirdiğinde bu kimlikle üçü eşleştirilebilir.
