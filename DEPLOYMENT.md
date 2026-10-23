# Production Deploy — Coolify (GitHub + otomatik domain)

Bu rehber backend'i Coolify ile sunucuya (45.87.120.58) deploy eder.
Mimari: **Coolify Application (Go container)** + **Coolify managed PostgreSQL** + **Traefik (otomatik SSL'li domain)**.

> Port derdi yok: Coolify her uygulamayı Traefik reverse-proxy arkasında domain bazlı yönlendirir; container içinde 8080 dinlemen yeterli.

> Bu sürümü (backend sprint 1) canlıya alıyorsan önce en sondaki [Backend sprint 1 sürüm notları ve deploy kontrol listesi](#backend-sprint-1-sürüm-notları-ve-deploy-kontrol-listesi) bölümünü oku.

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

> `USAGE_TIMEZONE`, sunucudaki bütün "bugün" hesaplarının gün sınırıdır: günlük kullanım sayaçları, giriş serisi ve giriş günleri, tekrar listesi ve `/counts` içindeki `today`, yanlış cevaptan sonraki "ertesi gün". Sürecin genel saat dilimini değiştiren `TZ` değişkenini **girme**; gün hesapları ona bakmaz, yalnızca log saatlerini değiştirir ve iki ayarın farklı görünmesi kafa karıştırır.

> **Apple revoke anahtarları:** `APPLE_TEAM_ID`, `APPLE_KEY_ID` ve `APPLE_PRIVATE_KEY` üçü birden girilmezse hesap silinirken Apple'daki uygulama yetkisi iptal **edilmez** (silme yine çalışır, açılış loguna uyarı düşer). Apple bu iptali şart koşar ve hesap silme sayfası bunu vaat eder. İptal için gereken Apple refresh token'ı yalnızca anahtarlar girildikten **sonra** yapılan Apple girişlerinde saklanır; anahtarları ilk yayından önce gir.

> **Hız sınırları:** `API_RATE_LIMIT` ve `AUTH_RATE_LIMIT` için `0` ya da negatif değer sınırı kapatmaz; uyarı loglanır ve varsayılana (600 / 10) dönülür.

### İstemci IP'si, Traefik ve hız sınırı

Hız sınırları istemci IP'sine (kimliği doğrulanmış genel uçlarda kullanıcıya) göre sayılır. IP, `X-Forwarded-For` başlığından **yalnızca** bağlantı `TRUSTED_PROXIES` içindeki bir adresten geliyorsa okunur; başlıkta birden çok adres varsa ilk geçerli IP alınır. Doğrudan internetten gelen bir bağlantının başlığı yok sayılır, bağlantının karşı ucu kullanılır.

Bu kurgu Coolify'nin Traefik'inin varsayılan davranışına dayanır: Traefik, güvenmediği kaynaktan (internetteki istemci) gelen `X-Forwarded-*` başlıklarını siler ve `X-Forwarded-For`'u istemcinin gerçek adresiyle yeniden yazar. Bunun için:

- Coolify → **Servers → Proxy** yapılandırmasında `--entrypoints.https.forwardedHeaders.insecure=true` **olmamalı**; `forwardedHeaders.trustedIPs` de **boş** kalmalı; önde Cloudflare olsa bile (aşağıdaki Cloudflare notuna bak: güvenilen bir ön proxy istemcinin uydurduğu adresi zincirin başında bırakır).
- Uygulama container'ının 8080 portu host'a **yayınlanmamalı** (Coolify'de "Ports Mappings" boş, yalnızca "Ports Exposes: 8080"). Yayınlanırsa docker ağ geçidinden (özel adres) gelen bağlantı güvenilir sayılır.
- `TRUSTED_PROXIES`'i daraltmak istersen Traefik'in bağlandığı ağın CIDR'ını kullan: sunucuda `docker network inspect coolify -f '{{range .IPAM.Config}}{{.Subnet}}{{end}}'`.

Doğrulama (deploy sonrası, uydurma başlıkla): 11. istekte `429` gelmeli. Gelmiyorsa Traefik istemci başlığını koruyordur.

```bash
for i in $(seq 11); do
  curl -s -o /dev/null -w '%{http_code}\n' -X POST https://<domain>/api/v1/auth/google \
    -H "X-Forwarded-For: 10.9.8.$i" -H 'Content-Type: application/json' -d '{"idToken":"x"}'
done
```

**Cloudflare'e geçilirse:** Traefik Cloudflare'e güvenmediği sürece `X-Forwarded-For` yalnızca Cloudflare kenar IP'sini taşır ve tüm kullanıcılar aynı kovaya düşer. Önerilen yol `PROXY_HEADER=CF-Connecting-IP`'dir: Cloudflare bu başlığı her istekte kendisi yazar (istemcinin gönderdiğini ezer). Bu durumda sunucuya **yalnızca** Cloudflare üzerinden ulaşılabildiğinden emin ol (firewall'da 80/443'ü Cloudflare aralıklarına kısıtla); yoksa doğrudan sunucuya gelen biri başlığı uydurabilir.

> **Uyarı:** Traefik'te `forwardedHeaders.trustedIPs`'e Cloudflare aralıklarını girip `PROXY_HEADER=X-Forwarded-For` ile devam etmek hız sınırını yeniden **atlatılabilir** yapar. Cloudflare istemcinin gönderdiği `X-Forwarded-For`'u silmez, gerçek adresi sonuna ekler (`uydurma, gerçek-istemci`); Traefik de Cloudflare'e güvendiği için başlığı korur. Sunucu zincirdeki **ilk** geçerli adresi aldığından (Fiber'ın davranışı) anahtar istemcinin uydurduğu adres olur. Bu yolu kullanma; `CF-Connecting-IP`'yi kullan.

### Açılış denetimi

Sunucu açılırken yapılandırmayı denetler. Şu durumlarda **açılmaz** (deploy logunda `config: ...` satırı ve sebebi görünür; Coolify eski container'ı çalıştırmaya devam eder):

- `APP_ENV` tanımsız ya da `production`/`development` dışında bir değer.
- Production'da `JWT_SECRET` 32 bayttan kısa, kod varsayılanı ya da örnek dosyalardaki yer tutuculardan biri.
- Production'da sözlük tablosu boş ve imajdaki seed yüklenemiyor (logda `sözlük yüklenemedi`).

Şunlar açılışı engellemez ama production'da açılış loguna `config: uyarı:` satırı düşer ve Sentry'ye uyarı olarak gider:

- `APPLE_TEAM_ID`/`APPLE_KEY_ID`/`APPLE_PRIVATE_KEY`'den biri eksik (hesap silmede Apple token iptali yapılmaz) ya da anahtar geçersiz.
- `GOOGLE_CLIENT_IDS` boş (her Google girişi reddedilir).
- `TEST_MODE_UNLOCK_ALL=true` (tüm gruplar kilitsiz açılır, reklam kapıları devre dışı).

Apple girişi açıkken (`APPLE_CLIENT_ID` dolu) revoke yapılandırılamadıysa açılış loguna ayrıca `!!! UYARI !!!` ile başlayan bir satır düşer ve Sentry'ye hata olarak gider: bu durumda hesap silme Apple'ın şart koştuğu token iptalini yapamaz (App Store 5.1.1(v)).

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

---

## Backend sprint 1 sürüm notları ve deploy kontrol listesi

Bu bölüm, `6b85321`'den sonraki backend sprint 1 değişikliklerini canlıya almak içindir. Bütün API değişiklikleri geriye dönük uyumludur: yayındaki mobil istemci hiçbir şey göndermeden eskisi gibi çalışır. Yeni istek alanları isteğe bağlıdır, yanıtlara yalnızca alan eklenmiştir.

### Yeni ve değişen ortam değişkenleri

Ayrıntılar ve önerilen değerler için 4. bölümdeki tabloya bak. Deploy'dan **önce** Coolify → Application → Environment'ta şunları kontrol et:

| Değişken | Ne değişti | Deploy öncesi yapılacak |
|---|---|---|
| `APP_ENV` | Artık **zorunlu ve katı**. Eskiden boşsa `development` sayılıyordu. Şimdi yalnızca tam `production` ya da `development` kabul edilir; boş ya da başka bir değerde (`prod`, `Production`) sunucu **açılmaz** | Değerin tam `production` olduğunu gör |
| `JWT_SECRET` | Production'da en az 32 bayt olmalı; kod varsayılanı ya da `.env*.example`'daki yer tutucular reddedilir, sunucu açılmaz | Uzunluğu kontrol et (`printf %s "$JWT_SECRET" \| wc -c`). Kısaysa `openssl rand -base64 48` ile yenisini gir. Değiştirmek yalnızca access token'ları (15 dk) geçersiz kılar: refresh token'lar JWT değil, veritabanındaki rastgele değerlerdir; istemci 401 alınca sessizce yeniler |
| `GATE_ENFORCEMENT` | Yeni. `off` / `log` / `enforce`, varsayılan `log`. Başka bir değerde sunucu açılmaz | İlk deploy'da `log` bırak (aşağıdaki sıraya bak) |
| `DB_MAX_OPEN_CONNS`, `DB_MAX_IDLE_CONNS`, `DB_CONN_MAX_LIFETIME` | Yeni; havuz ayarları env'den gelir. Varsayılanlar 25 / 10 / 1h (boşta tutulan bağlantı eskiden sabit 5'ti) | Genelde boş bırakılabilir. Toplamın PostgreSQL `max_connections`'ı aşmadığını gör |
| `DB_STATEMENT_TIMEOUT` | Yeni, varsayılan `10s`. Her sorguya PostgreSQL'de üst süre uygulanır (eskiden sınırsızdı). Migration ve sözlük seed'i bu sınırdan muaftır | Varsayılan yeterli. Sorgu zaman aşımı yüzünden 500'ler görülürse artır ya da `0` ile kapat |
| `TRUSTED_PROXIES`, `PROXY_HEADER` | Yeni. `X-Forwarded-For` yalnızca güvenilen proxy'den (varsayılan: özel ağlar + loopback) gelen bağlantıda okunur | Boş bırak. Traefik doğrulaması için aşağıdaki canlı kontrollere bak |
| `SENTRY_RELEASE` | Boşsa sürüm, derlemede `-ldflags "-X main.version=<commit>"` ile gömülen commit olur (Dockerfile `SOURCE_COMMIT` build arg'ı) | Boş bırak; Coolify'de "Include Source Commit in Build" açık olmalı |
| `SHUTDOWN_TIMEOUT` | Yeni, varsayılan `8s`. SIGTERM'de süren istekler bu kadar beklenir | Boş bırak (Notlar'daki düzgün kapanma maddesine bak) |
| `API_RATE_LIMIT` | Anlamı değişti: geçerli access token'lı istekler artık **kullanıcı başına**, diğerleri IP başına sayılır | Değer aynı kalabilir (`600`) |

### Açılışta AutoMigrate'in uygulayacağı veritabanı değişiklikleri

Kaynak: `git diff 6b85321..HEAD -- internal/models`. Yalnızca `refresh_tokens` tablosu değişir; diğer tablolar aynıdır (`word_progresses.wrong_count`'ta yalnızca kod yorumu değişti):

| Tablo | Değişiklik | Not |
|---|---|---|
| `refresh_tokens` | `family_id varchar(32) NULL` kolonu | Aynı girişten türeyen token'ları bir oturumda toplar. Eski satırlarda boştur, ilk rotasyonda doldurulur |
| `refresh_tokens` | `idx_refresh_tokens_family_id` index'i (`family_id`) | Logout ve yeniden kullanım tespitinde ailenin iptali için |
| `refresh_tokens` | `replaced_by_id bigint NULL` kolonu | Rotasyonla yerine geçen satır; yanıtı kaybolan yenilemenin tolerans süresinde yeniden denenebilmesi için |

- Kolonlar boş bırakılabilir (nullable) ve varsayılansızdır: ekleme anlıktır, veri taşıması yoktur. Index `CONCURRENTLY` olmadan kurulur ve kurulurken `refresh_tokens`'a yazmayı kısa süre bekletir; tablo küçük olduğundan saniyeden kısa sürer.
- Migration tek bağlantıda ve `statement_timeout` kapalıyken çalışır. Başarısız olursa yeni container hiç ayağa kalkmaz, Coolify eskisini çalıştırmaya devam eder.
- Yeni sunucu saatlik bir **refresh token temizliği** çalıştırır: süresi ya da iptali 7 günden eski satırlar silinir. İlk tur açılışta çalışır ve birikmiş eski satırları bir kerede siler (logda `refresh token temizliği: N satır silindi`). Birden çok replika aynı anda çalıştırırsa advisory lock sayesinde yalnızca biri siler. Kapanmada tur kesilir, veritabanı havuzundan önce durdurulur.
  - **Deploy öncesi say:** Temizlik bugüne kadar hiç çalışmadı ve ilk tur tek bir `DELETE` ile çalışır; bu sorgu da `DB_STATEMENT_TIMEOUT`'a (varsayılan `10s`) ve tur başına 1 dakikalık sınıra tabidir. Silinecek satır sayısını önceden gör:
    `SELECT count(*) FROM refresh_tokens WHERE expires_at < now() - interval '7 days' OR revoked_at < now() - interval '7 days';`
    Sayı birkaç yüz binin üstündeyse ilk temizliği deploy'dan önce elle, parçalar halinde yap (`DELETE FROM refresh_tokens WHERE id IN (SELECT id FROM refresh_tokens WHERE <aynı koşul> LIMIT 50000);` sorgusunu 0 satır dönene kadar tekrarla). Yapılmazsa ve silme 10 sn'yi aşarsa her saat `refresh token temizliği başarısız ... canceling statement due to statement timeout` hatası Sentry'ye düşer ve tablo hiç küçülmez (istekler etkilenmez).
- İsteğe bağlı, deploy'un parçası değil (ürün kararı): eski 2200 eşiğiyle erken verilmiş `level_B2` rozetleri `earned_badges`'te durur. Temizlemek için SQL, sprint raporundaki BB ertelenen maddesindedir. Bir rozeti geri almak demek olduğu için ürün onayı olmadan çalıştırma.

### Davranış değişiklikleri

İstemciyi bozmazlar ama destek ve izleme sırasında bilinmeleri gerekir:

- **Leitner, yanlış cevap:** Tekrardaki kelime bir alt kutuya iner (en alt kutu 2) ve `next_review_date` ertesi kullanım gününün başı olur (`USAGE_TIMEZONE` gece yarısı). Eskiden kelime ancak üçüncü art arda yanlışta bir alt kutuya iniyor ve kutusunun aralığı kadar sonra soruluyordu. `wrongCount` yalnızca art arda yanlış sayısıdır (istatistik), kutuyu etkilemez. Doğru cevap kuralları değişmedi.
- **Öğrenme (`POST /progress/learn`) idempotent:** Kelime sözlükten doğrulanır, sözlükte yoksa `404 unknown_word` döner. Zaten ilerlemiş bir kelimeyi kutu 1'e sıfırlamaz; aynı istek tekrar gelirse ilerleme değişmez.
- **Swipe (`POST /progress/swipe`) idempotent:** Kelime satırı kilitlenir; vadesi gelmemiş kelimedeki cevap değişiklik yapmaz. Çift dokunuş ya da yeniden deneme kelimeyi iki kutu ilerletmez.
- **`POST /users/me/advance-group`:** İsteğe bağlı gövde `{"level","group"}` (tamamlanan grup) ile idempotenttir: işaretçi yalnızca o gruptaysa bir ileri gider. Gövdesiz istek eski davranıştır ama satır kilidiyle atomiktir. C2'nin son grubundan sonra A1 Grup 1'e geri sarma olmaz.
- **`PUT /users/me/current-group`:** Seviye/grup doğrulanır (aksi `400 invalid_group`); işaretçi yalnızca **ileri** yazılır, geriye dönük istek değişiklik yapmadan 200 döner.
- **Refresh toleransı:** Rotasyonla iptal edilmiş token 60 sn içinde ve halefi hiç kullanılmamışsa yeniden kabul edilir (yanıtı mobil ağda kaybolan yenileme). Bunun dışındaki yeniden kullanım çalınmış token sayılır: token'ın **bütün ailesi** iptal edilir, 401 döner. 401 yalnızca token reddedilince döner; veritabanı hatası 500'dür (istemci oturumu koruyup yeniden dener).
- **Logout:** Verilen token'la birlikte aynı oturumdan (aile) türeyen bütün token'ları iptal eder.
- **Access token:** `exp` zorunludur; yeni token'lar `iss`/`aud` taşır. `iss`/`aud`'suz eski token'lar süreleri dolana kadar kabul edilir.
- **Health:** `/api/v1/health` veritabanını 2 sn'lik zaman aşımıyla sınar; ulaşılamazsa `503 db_unavailable` döner ve container `unhealthy` olur.
- **Hız sınırı:** Kimlikli istekler kullanıcı başına sayılır (aynı CGNAT IP'sini paylaşan kullanıcılar birbirinin sınırını doldurmaz). `X-Forwarded-For` yalnızca güvenilen proxy'den okunur. Giriş uçlarının sıkı sınırı (`AUTH_RATE_LIMIT`) IP başınadır.
- **Hatalar sahte başarı dönmez:** İlerleme, harita, ana sayfa, istatistik, `/auth/me` ve parola girişi uçlarında veritabanı hatası artık 500 döner; eskiden bazıları 200 (boş liste/sıfır), 404 `user_not_found` ya da 401 dönüyordu. Bağlı giriş yöntemleri okunamazsa `linkedProviders: []` yerine 500 döner. Bu yüzden deploy sonrası Sentry'de 5xx sayısı artmış görünebilir: bunlar önceden gizlenen hatalardır.
- **Günler:** Giriş günü ve seri dahil bütün "bugün" hesapları `USAGE_TIMEZONE`'a göredir (varsayılan Europe/Istanbul). Gece yarısı ile 03:00 arasındaki girişler artık doğru güne yazılır.
- **Rozetler ve toplamlar:** Seviye rozeti hedefleri sözlükteki gerçek kelime sayısıdır; `GET /stats/summary` yanıtına `levelTotals` eklendi. Yeni rozet yalnızca gerçekten eklendiğinde bildirilir.
- **Tekrar listesi:** `GET /progress/review/box/:box` isteğe bağlı `?limit=N` (1–200) alır; sıra sabittir (`next_review_date ASC NULLS FIRST`, sonra `id`). Listeyle `/counts`'taki `today` aynı kümedir.
- **Sözlük batch'i:** `POST /dictionary/words/batch` en fazla 500 tekil id kabul eder (fazlası `400 invalid_request`); boş ve tekrar eden id'ler yok sayılır. İstemci tek seferde 50 id gönderir.
- **Hesap silme ve Apple:** `DELETE /auth/account` isteğe bağlı `{"appleAuthorizationCode"}` alır; saklı token yoksa bu kodla iptal yapılır. Apple bağlantısı kaldırılırken saklı token iptal edilir. İptal başarısız olsa da silme sürer, hata Sentry'ye gider.
- **Sosyal kayıt:** Google/Apple'dan gelen görünen ad temizlenir ve 60 karaktere kısaltılır. Eşzamanlı ilk girişte 500 yerine aynı kullanıcı döner.
- **Gözlemlenebilirlik:** Her yanıtta `X-Request-ID` başlığı vardır; 5xx'ler istek kimliğiyle loglanır ve Sentry'ye gider. SIGTERM'de süren istekler beklenir (düzgün kapanma).
- **Reklam kapısı (`GATE_ENFORCEMENT`):** `log` kipinde ihlal loglanır ve Sentry'ye bilgi olarak gider, istek kabul edilir. `enforce` kipinde öğrenme `403 gate_required`, tekrar cevabı `403 review_gate_required` ile reddedilir.

### Güvenli yayın sırası

1. **Hazırlık:** CI'da son commit yeşil (`test` ve `docker` işleri). Ortam değişkenleri yukarıdaki tabloya göre kontrol edildi; `GATE_ENFORCEMENT` açıkça `log`.
2. **Deploy öncesi yedek:** PostgreSQL kaynağı → Backups → **Backup Now**, satır "Success" olmalı. Migration geri alınabilir bir değişiklik olsa da sprint sonrası ilk deploy öncesinde elde taze bir dump bulunsun.
3. **Önce backend:** Backend'i deploy et. Yayındaki istemci için güvenlidir: yeni alanları göndermese de eski davranış korunur. Mobil istemcinin yeni sürümü (advance-group gövdesi, `?limit=50`, Apple silme kodu, kapı kodlarının işlenmesi) backend canlıdayken yayınlanmalıdır, **önce değil**. Yeni istemci eski backend'e karşı da çalışır ama sunucu tarafı korumalardan yararlanamaz.
4. **İzle (en az birkaç gün):** "Yayın sonrası doğrulama" listesini ve aşağıdaki canlı kontrolleri uygula. Sentry'de şunlara bak: yeni 5xx türleri (özellikle sorgu zaman aşımı, `canceling statement due to statement timeout`), kapı ihlali bilgileri (`log` kipinde), `!!! UYARI !!!` satırı ya da `refresh token temizliği başarısız`.
5. **Sonra enforce:** Yeni istemci yaygınlaştıktan ve `log` kipinde yeni sürümden gelen ihlal kalmadığı görüldükten sonra `GATE_ENFORCEMENT=enforce` yapıp uygulamayı yeniden başlat (redeploy gerekmez). Eski istemci `gate_required` / `review_gate_required` kodlarını tanımaz, ham hata gösterir. Eski sürümün payı hâlâ yüksekse enforce'a geçme ya da önce zorunlu güncelleme kararını ver.

### Geri alma (rollback)

- **Yalnızca kapı sorunu:** `GATE_ENFORCEMENT=log` (ya da `off`) yapıp yeniden başlat. Kod geri alınmaz.
- **Sorgu zaman aşımı kaynaklı 500'ler:** `DB_STATEMENT_TIMEOUT`'u artır ya da `0` yap, yeniden başlat.
- **Kodu geri almak:** Coolify'de bir önceki commit'i deploy et. Şema geri alma gerekmez: yeni kolonlar boş bırakılabilir, eski kod onları yok sayar (AutoMigrate kolon silmez). Eski sunucu yeni access token'ları kabul eder (`iss`/`aud`'a bakmaz), refresh token'lar geçerli kalır, kullanıcılar çıkış yapmaz. Geri alınca refresh toleransı, aile iptali ve saatlik temizlik de geri gider; oturumlar eski kurallarla sürer.
- **Geri alırken dikkat:** Eski kod `APP_ENV` boşken `development` sayar; değişkeni hiçbir zaman kaldırma (production'da parola uçları açılır). Geri alınan sürüm Leitner'in eski yanlış cevap kuralına (üç yanlışta bir alt kutu) döner; aradaki günlerde yazılmış kutu/tarihler olduğu gibi kalır, düzeltme gerekmez.
- **Veri bozulması:** Yalnızca gerçek bir veri kaybı ya da bozulmasında 2. adımdaki dump'tan geri yükle ([BACKUP.md](BACKUP.md), "Gerçek bir felaketten dönüş"). Dump'tan sonraki ilerleme kaybolur.

### Canlıda elle yapılacak kontroller

Bunlar kodla ya da CI'da doğrulanamaz; ilk deploy'dan sonra bir kez yapılıp sonuç kaydedilmeli:

1. **Traefik `X-Forwarded-For`'u siliyor mu:** "İstemci IP'si, Traefik ve hız sınırı" bölümündeki 11 uydurma `X-Forwarded-For`'lu istek, 11.'de `429` ile bitmeli. Bitmiyorsa Traefik istemci başlığını koruyordur ve IP başına sınır atlatılabilir.
2. **Health probe:** Coolify'nin arayüz health check'i kapalı olmalı; sunucuda `docker ps` çıktısında container `(healthy)` görünmeli (imajın `HEALTHCHECK`'i, `/server -healthcheck`). `curl -si https://<domain>/api/v1/health` → `200` ve `"dictionaryWords":9980`.
3. **Sürüm gömülü mü:** Coolify'de "Include Source Commit in Build" açık; açılış logunda `kelimeler-backend sürüm: <commit>` (`bilinmiyor` değil) ve Sentry olaylarında aynı release.
4. **S3 yedeği ve geri yükleme provası:** Zamanlanmış yedek "Success", dosya bucket'ta; ilk geri yükleme provası `scripts/restore-verify.sh` ile yapılıp kaydedildi ([BACKUP.md](BACKUP.md)). Saklama süreleri, herkese açık sayfaların verdiği sözle uyumlu olmalı: hesap silme ve gizlilik sayfaları silinen hesabın yedeklerdeki kopyalarının **en fazla 14 gün** içinde silindiğini söyler. Coolify S3 saklaması ve bucket yaşam döngüsü kuralı 14 günü aşmamalı.
5. **Açılış logu:** `ortam: APP_ENV=production GATE_ENFORCEMENT=log`, `db: migrate tamamlandı`, `kullanım günü saat dilimi: Europe/Istanbul` satırları var; `config: uyarı:` ve `!!! UYARI !!!` satırları yok.
6. **GitHub:** Settings → Branches'ta `main` için `test` ve `docker` zorunlu kontrol (kod dışı repo ayarı).
7. **Herkese açık sayfalar:** Flutter reposundaki `docs/` güncellendiyse `make sync-web` çalıştırılıp commit edildi; deploy sonrası `/privacy` ve `/account-deletion` sayfalarındaki "Son Güncelleme" tarihi Flutter'daki ile aynı ("Herkese açık sayfalar" bölümüne bak).
