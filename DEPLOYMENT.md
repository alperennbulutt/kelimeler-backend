# Production Deploy — Coolify (GitHub + otomatik domain)

Bu rehber backend'i Coolify ile sunucuya (45.87.120.58) deploy eder.
Mimari: **Coolify Application (Go container)** + **Coolify managed PostgreSQL** + **Traefik (otomatik SSL'li domain)**.

> Port derdi yok: Coolify her uygulamayı Traefik reverse-proxy arkasında domain bazlı yönlendirir; container içinde 8080 dinlemen yeterli.

> Bu sürümü (backend sprint 1) canlıya alıyorsan önce en sondaki [Backend sprint 1 sürüm notları ve deploy kontrol listesi](#backend-sprint-1-sürüm-notları-ve-deploy-kontrol-listesi) bölümünü oku.
>
> E-posta/parola girişi ve misafir hesabını içeren sürümü canlıya alıyorsan ayrıca [E-posta/parola girişi ve misafir hesabı](#e-postaparola-girişi-ve-misafir-hesabı-sürüm-notları-ve-deploy-kontrol-listesi) bölümünü oku.

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
| `GUEST_REFRESH_TOKEN_TTL` | `8760h` (1 yıl, varsayılan) — misafir hesaplarının refresh token ömrü, rotasyonda da. Misafirin başka giriş yolu yoktur; token kaybolur ya da süresi dolarsa hesaba bir daha ulaşılamaz. Misafir hesabını kaydedince sonraki rotasyonlar `REFRESH_TOKEN_TTL`'e döner. Sıfır ya da negatif değerde sunucu açılmaz |
| `GOOGLE_CLIENT_IDS` | `928054180858-...apps.googleusercontent.com` |
| `APPLE_CLIENT_ID` | iOS bundle id: `com.kaipra.kutularlaIngilizce` (Apple yoksa boş). Apple kimlik token'ının `aud` alanı buna eşit olmalı; farklıysa Apple girişi 401 verir. |
| `APPLE_TEAM_ID` | Apple Developer hesabındaki 10 karakterlik Team ID — **iOS yayını için zorunlu** (aşağıdaki nota bak) |
| `APPLE_KEY_ID` | "Sign in with Apple" için üretilen anahtarın Key ID'si — **iOS yayını için zorunlu** |
| `APPLE_PRIVATE_KEY` | O anahtarın `.p8` içeriği (satır sonları `\n` olarak kaçırılabilir) — **iOS yayını için zorunlu**, gizli |
| `USAGE_TIMEZONE` | `Europe/Istanbul` — günlük kullanım sayaçları bu saat diliminin gece yarısında sıfırlanır (girilmezse varsayılan da budur) |
| `GATE_ENFORCEMENT` | `log` (varsayılan). Reklam kapısı ihlallerinde sunucunun tepkisi: `off` denetlemez, `log` ihlali loglayıp Sentry'ye bilgi olarak gönderir ama isteği kabul eder, `enforce` isteği `403 gate_required` / `review_gate_required` ile reddeder. `enforce`'a ancak istemcinin bu kodları işleyen sürümü yaygınlaştıktan ve `log` kipinde ihlal kalmadığı görüldükten sonra geç. Başka bir değerde sunucu açılmaz |
| `API_RATE_LIMIT` | `600` — genel API hız sınırı, istek/dk. Geçerli access token'lı istekler kullanıcı başına, diğerleri IP başına sayılır. Kimliksiz uçlarda aynı IP'yi paylaşan (CGNAT) kullanıcılar sınırı paylaştığı için düşük tutma |
| `TRUSTED_PROXIES` | **Boş bırak** (varsayılan: özel ağlar + loopback). İstemci IP başlığına yalnızca bu adreslerden gelen bağlantıda güvenilir. Daraltmak istersen Coolify ağının CIDR'ını gir (aşağıya bak) |
| `PROXY_HEADER` | `X-Forwarded-For` (varsayılan). Cloudflare'e geçilirse aşağıdaki nota bak |
| `AUTH_RATE_LIMIT` | `10` — giriş/kayıt uçlarının (Google/Apple, e-posta/parola, e-posta sorgusu, misafir, parola bağlama) sıkı hız sınırı, IP başına istek/dk; hepsi tek kovayı paylaşır. E-posta önce akışında bir giriş iki istektir (sorgu + giriş/kayıt). `/auth/refresh` buna değil `API_RATE_LIMIT`'e tabidir |
| `SENTRY_DSN` | Sentry projesinin DSN'i. Boşsa hata raporlama kapalıdır |
| `SENTRY_RELEASE` | Boş bırakılabilir: imaj derlenirken gömülen sürüm (commit) kullanılır. Elle verilirse o geçerlidir |
| `SENTRY_RELAY_DSN` | Mobil uygulamanın **gerçek** Sentry DSN'i (`https://ANAHTAR@o….ingest.us.sentry.io/PROJE_NO`). Uygulamanın hata raporlarını Sentry'ye aktaran röleyi açar; boşsa röle kapalıdır. Bkz. "Sentry rölesi" |
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
- `MIN_APP_VERSION_IOS` ayarlı ama `APP_STORE_URL_IOS` boş ("Güncelle" butonu App Store'un ana sayfasını açar).

Zorunlu güncelleme ayarlarındaki (`MIN_/LATEST_APP_VERSION_*`, `APP_STORE_URL_*`) geçersiz bir değer de açılışı engellemez: **her ortamda** `config: uyarı:` satırı düşer, Sentry'ye gider ve değer yok sayılır (o sınır uygulanmaz). Bkz. "Zorunlu güncelleme".

Apple girişi açıkken (`APPLE_CLIENT_ID` dolu) revoke yapılandırılamadıysa açılış loguna ayrıca `!!! UYARI !!!` ile başlayan bir satır düşer ve Sentry'ye hata olarak gider: bu durumda hesap silme Apple'ın şart koştuğu token iptalini yapamaz (App Store 5.1.1(v)).

### Production'a özgü davranışlar

`APP_ENV=production` iken sunucu bilerek farklı davranır:

- **CORS:** `CORS_ALLOWED_ORIGINS` boşsa CORS middleware'i hiç eklenmez; yanıtlara `Access-Control-*` başlığı yazılmaz ve tarayıcılar başka bir siteden gelen isteğin yanıtını okutmaz. Mobil istemci CORS'tan etkilenmez. (Geliştirmede liste boşken tüm origin'lere izin verilir.)
- **Sayaç sıfırlama ucu yoktur:** `POST /api/v1/users/me/usage/reset` yalnızca production dışında açıktır.

### Giriş yolları ve e-postayla hesap eşleme (her ortamda aynı)

- **Google, Apple, e-posta/parola ve misafir** girişleri her ortamda açıktır (`/auth/google`, `/auth/apple`, `/auth/register`, `/auth/login`, `/auth/guest`); e-posta önce giriş ekranının adres sorgusu (`/auth/email/lookup`) da. Hepsi IP başına sıkı sınıra (`AUTH_RATE_LIMIT`) tabidir.
- Google/Apple'ın **doğrulamadığı** e-posta hiçbir hesapla eşlenmez; kimlik yer tutucu adresli (`<sağlayıcı>_<sub>@users.kelimeler.local`) yeni bir hesaba bağlanır.
- E-posta/parola kaydında adres sahipliği **doğrulanmaz** (e-posta gönderme altyapısı yok; parola sıfırlama da yok). Bu yüzden: doğrulanmış bir Google/Apple e-postası, **tek giriş yolu parola olan** hesapla eşleşirse hesap o kimliğe devredilir: parola kimliği silinir, kullanıcının bütün refresh token'ları iptal edilir (diğer cihazlar ilk yenilemede çıkış yapar), sosyal kimlik eklenir. Hesabı açan gerçekten adresin sahibiyse ilerlemesi korunur; başkasıysa erişimi biter. Log satırı: `auth: doğrulanmış <sağlayıcı> e-postası parola hesabını devraldı ... (user=<id>)`.
- **Devirden ve geri almadan önce verilmiş access token'lar:** refresh token'lar iptal edilse de access token (JWT) imzası geçerli olduğu için `ACCESS_TOKEN_TTL` (15 dk) boyunca kabul edilir. Devirde ve geri almada hesabın oturum sürümü (`users.token_version`, access token'da `tv` claim'i) artar; **hesabı denetleyen uçlar** (`POST/DELETE /auth/link/*`, `POST /auth/link/password`, `DELETE /auth/account`) eski sürümlü token'ı `401 unauthenticated` ile reddeder. Bu olmasaydı adresi parolayla önceden kapatan kişi, hesap sahibine devredildikten sonra elindeki access token'la kendi Apple/Google'ını o hesaba bağlayıp ona kalıcı olarak girebilir, sahibin giriş yolunu kaldırabilir ya da hesabı silebilirdi. Diğer uçlar (ilerleme, profil) eski token'ı süresi dolana kadar kabul eder: en fazla `ACCESS_TOKEN_TTL` boyunca okuma/yazma, kalıcı erişim değil. `tv`'siz eski token'lar `0` sayılır; hiç devredilmemiş hesaplarda çalışmaya devam eder. Devirle aynı anda süren bir bağlama da devri bekletir: bağlama önce biterse hesabın parolanın yanında başka bir giriş yolu vardır ve devir yerine adres geri alınır.
- Parolanın yanında başka bir giriş yolu (ör. Apple) varsa hesap devralınmaz; adresin hesaptaki dayanağına bakılır (ayrıntı ve gerekçe: aşağıda "Adresin geri alınması"):
  - Hesabın Google/Apple kimliklerinden biri **aynı adresi doğrulamışsa** (`auth_identities.email_verified`) adres o hesabın sahibinindir: gelen başka sağlayıcı kimliği hesaba bağlanır, parola ve oturumlar kalır. Aynı sağlayıcının farklı bir kimliğiyse (başka subject) `409 email_in_use`.
  - Değilse hesabın adrese tek dayanağı doğrulanmamış paroladır ve **adres geri alınır:** hesabın parola kimliği silinir, bütün refresh token'ları iptal edilir, adresi yer tutucuya (`reclaimed_<id>@users.kelimeler.local`, istemciye `email: ""`) çevrilir; adresin doğrulanmış sahibine o adresle **yeni bir hesap** açılır ve oturum verilir. Eski hesap verisini ve kalan Google/Apple kimliğini korur, onunla girmeye devam eder.
- Parolasız hesapta aynı sağlayıcının başka bir kimliği varsa `409 email_in_use`; yoksa kimlik o hesaba bağlanır.
- Yer tutucu adresler (`@users.kelimeler.local`) istemciye `email: ""` olarak döner ve kayıtta/bağlamada `400 invalid_email` ile reddedilir.

#### Adresin geri alınması

Parola kaydında adres sahipliği doğrulanmaz. Eskiden, adresi (X) parolayla önceden kapatıp yanına **kendi** Google/Apple hesabını bağlayan biri, X'in gerçek sahibinin Google/Apple girişini kalıcı olarak `409 email_in_use`'a düşürüyordu. Ürün kararı (26 Ekim): adres doğrulanmış sahibine geri verilir.

- **Neden sahibi o hesaba bağlanmıyor:** parolanın yanındaki kimliği kimin tuttuğu bilinmez; bağlamak, adresi kapatan kişiye sahibin hesabına giriş vermek olurdu. Bu yüzden sahibe ayrı, yeni bir hesap açılır.
- **Neden "kanıtlı" hesapta geri alma yok:** hesabın bir Google/Apple kimliği X'i doğrulamış olarak kaydettiyse, X'in sahibi o hesaptadır (meşru durum: parolayla kaydolup sonra kendi Google'ını bağlamış kişi). Kanıt yalnızca kimlik açılırken sağlayıcının doğruladığı adrestir; parola kimliğinin adresi hiçbir zaman kanıt sayılmaz.
- **Ne yapılır (tek transaction):** hesabın kimlikleri kilitlenir, parola kimliği silinir, etkin refresh token'lar kilitlenip iptal edilir (aynı anda süren rotasyonun halefi de; devirdeki yarış düzeltmesiyle aynı), adres yer tutucuya çevrilir, sonra yeni hesap ve kimlik yazılır. Aynı anda süren parola girişi ya geri almayı bekletir ve verdiği token da iptal edilir, ya da geri almayı bekler ve `401` alır. Hesabın oturum sürümü (`users.token_version`) artar; önceden verilmiş access token'lar için aşağıdaki maddeye bak.
- **Doğrulanmamış adres hiçbir zaman geri almaz;** devir ve geri alma yalnızca sağlayıcının doğruladığı adresle olur.
- **Misafir bağlamasında (`/auth/link/google|apple`) geri alma yoktur:** X başka hesaptayken de bağlama başarılıdır, sahibi kimliğini bağlar ve ondan sonra onunla kendi hesabına girer; yalnızca adres hesaba yazılmaz. Geri almak, bir bağlama isteğinin üçüncü bir hesabın parolasını silmesi olurdu; o hesabın tek giriş yolu parolaysa hesap girişsiz kalırdı (ve o hesap sahibin önceden parolayla açtığı kendi hesabı olabilir). `/auth/link/password` zaten doğrulanmamış bir adrestir, geri alamaz.
- **`email_verified` ve var olan satırlar:** kolon eklenirken bütün eski satırlar `false` olur, Google/Apple kimlikleri de. `6b85321`'den önceki sunucu sağlayıcının doğrulamadığı adresi de kimliğe yazıyordu; eski satırdaki adresin doğrulanmış olduğu veriden kanıtlanamaz. Temkinli seçim `false`'tur: yanlışlıkla `true` saymak, X'i doğrulanmamış tutan bir hesaba X'in sahibinin girişini bağlatabilirdi (güvenlik açığı). `false` saymanın en kötü sonucu: eski bir **parola + kendi Google'ı** hesabına aynı adresle **üçüncü** bir sağlayıcıyla (ör. Apple) ilk kez giriş yapıldığında bağlama yerine geri alma olur; kişi Apple'la yeni bir hesaba girer, eski hesabına Google'la girmeye devam eder ama parolası ve adresi gider. Bunu önlemek için canlıdaki veritabanında `6b85321` ya da sonrası **baştan beri** çalışıyorsa (o sürümden beri sosyal kimliklere yalnızca doğrulanmış adres yazılır), o sürümün canlıya çıktığı andan sonra açılmış satırlar elle işaretlenebilir:
  ```sql
  UPDATE auth_identities SET email_verified = true
  WHERE provider IN ('google','apple') AND email IS NOT NULL
    AND email NOT ILIKE '%@users.kelimeler.local'
    AND created_at >= '<6b85321 içeren sürümün canlıya çıktığı an, UTC>';
  ```
  Tarihten emin değilsen **çalıştırma**; varsayılan `false` güvenli olandır.

---

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
5. **Kayıt ve e-posta sorgusu uçları açık, geliştirme uçları kapalı:** gövdesiz `POST /api/v1/auth/register` ve `POST /api/v1/auth/email/lookup` 400 dönüyor (404 ise canlıda e-posta/misafir sürümünden eski bir derleme çalışıyordur). Sayaç sıfırlama ucu `POST /api/v1/users/me/usage/reset` geçerli bir access token'la 404 dönmeli; 200 dönüyorsa `APP_ENV` `production` değildir. (`/auth/guest`'i doğrulama için çağırma: her çağrı gerçek bir misafir hesabı açar.)
6. **CORS kapalı** (`CORS_ALLOWED_ORIGINS` boşsa): `Origin` başlıklı isteğin yanıtında `Access-Control-Allow-Origin` yok.

```bash
# Başlıklar için token gerekmez:
curl -si https://<domain>/api/v1/health
# Kullanım alanları:
curl -s https://<domain>/api/v1/users/me/usage -H "Authorization: Bearer <access-token>"
# Kayıt ucu açık (gövdesiz istek 400 beklenir):
curl -s -o /dev/null -w '%{http_code}\n' -X POST https://<domain>/api/v1/auth/register
# E-posta sorgusu açık (gövdesiz istek 400 beklenir; AUTH_RATE_LIMIT'ten bir hak harcar):
curl -s -o /dev/null -w '%{http_code}\n' -X POST https://<domain>/api/v1/auth/email/lookup
# Sayaç sıfırlama kapalı (404 beklenir):
curl -s -o /dev/null -w '%{http_code}\n' -X POST https://<domain>/api/v1/users/me/usage/reset -H "Authorization: Bearer <access-token>"
# CORS (çıktı boş olmalı):
curl -si https://<domain>/api/v1/health -H 'Origin: https://ornek.example' | grep -i '^access-control-'
```

---

## 6) Kelime sözlüğü (otomatik yüklenir ve yamalanır)

Elle bir şey çalıştırmak gerekmez. Sözlük imajın içindedir (`/seed`). Uygulama her açılışta, migrate'ten sonra sırasıyla şunları yapar:

1. `dictionary_words` tablosu **boşsa** `/seed/dictionary_words.sql`'i yükler. Tablo doluysa ona dokunmaz.
2. `/seed/dictionary_patches/` altındaki henüz uygulanmamış **sözlük yamalarını** sırayla uygular (aşağıdaki "Sözlük yamaları" bölümü).

Doğrula:

- İlk açılışın deploy logunda `db: seed tamamlandı, 9980 kelime yüklendi` satırı ve ardından her yama için `db: sözlük yaması <ad> uygulandı (...)` satırı var. Yeni kurulumda seed yamaların değerlerini zaten taşıdığından bu satır `0 kart değişti` der. Canlıdaki mevcut sözlükte `0001_kritik_ve_a1` ilk kez uygulanırken `136 kart, 136 kart değişti` beklenir; `uyarı:` satırı çıkmamalı.

> **İlk deploy'da 8 yama birden uygulanır** (toplam 5534 kart; sözlüğün yarısından fazlası değişir). **Öncesinde mutlaka yedek al:** PostgreSQL kaynağı → Backups → **Backup Now**, satır "Success" olmalı. Yama geri alınabilir ama tek adımda geri dönüşün yolu bu yedektir. Logda sırasıyla şu satırlar beklenir (`N ilerleme satırının metni eşitlendi` kullanıcı sayısına göre değişir), hiçbir `uyarı:` satırı çıkmamalı:
>
> | Yama | Beklenen log |
> |---|---|
> | `0001_kritik_ve_a1` | `136 kart, 136 kart değişti` |
> | `0002_a2` | `474 kart, 474 kart değişti` |
> | `0003_b1` | `621 kart, 621 kart değişti` |
> | `0004_b2` | `1448 kart, 1448 kart değişti` |
> | `0005_c1` | `1701 kart, 1701 kart değişti` |
> | `0006_c2` | `1144 kart, 1144 kart değişti` |
> | `0007_tur2_kalanlar` | `8 kart, 8 kart değişti` |
> | `0008_a1_amerikan` | `2 kart, 2 kart değişti` |
>
> Sonra `SELECT name, words, updated FROM dictionary_patches_applied ORDER BY name;` 8 satır döner. İçeriği değişen kartlarda kullanıcının ilerlemesi (kutu, tarihler) o karta bağlı kalır, kartta artık yeni kelime görünür (aşağıda "Kullanıcı ilerlemesi").
- `https://<domain>/api/v1/health` yanıtında `"dictionaryWords":9980` görünüyor. Yamalar kart eklemez ya da silmez, bu sayı değişmez.
- PostgreSQL terminalinde: `SELECT name, words, updated, applied_at FROM dictionary_patches_applied ORDER BY name;`

> Çalışma imajı distroless'tır ve root olmayan kullanıcıyla (`nonroot`, uid 65532) çalışır: içinde kabuk ve `psql` yoktur, Application terminali açılmaz. Firebase import aracı (`/import`) çalışma imajında yoktur; gerekirse lokalde `make import-dictionary` ya da `docker compose run --rm import` (ayrı `import` build hedefi) kullanılır. Veritabanına elle bakmak gerekirse Coolify'de **PostgreSQL kaynağının** terminalini kullan (`psql -U postgres -d <veritabanı-adı>`).

### Sözlük yamaları (kelime düzeltmek)

Bir kartın İngilizcesini, Türkçesini ya da örnek cümlesini düzeltmenin olağan yolu budur. Kesinti, `TRUNCATE` ya da elle SQL gerekmez; deploy yeterlidir.

**Yeni yama eklemek:**

1. `seed/dictionary_patches/` altına bir sonraki sıra numarasıyla `NNNN_ad.json` ekle (ör. `0002_b1_ornekler.json`; ad küçük harf, rakam ve alt çizgiden oluşur). Biçim:
   ```json
   {
     "description": "Neden ve kaynak",
     "changes": [
       {"id": "A1_2_45",
        "old": {"english": "bag", "turkish": "makyaj çantası", "example": "My makeup bag is pink."},
        "new": {"english": "makeup bag", "turkish": "makyaj çantası", "example": "My makeup bag is pink."},
        "kind": "bilesik_kelime", "reason": "..."}
     ]
   }
   ```
   `new`'deki üç alan zorunludur ve boş olamaz. `old` isteğe bağlıdır ama verilmesi önerilir (aşağıda "uyarılar"). `kind`, `reason`, `review_note` yalnızca inceleme içindir. Başka bir alan (ör. `level`) dosyayı geçersiz kılar: yama yalnızca var olan kartın `english`, `turkish`, `example` alanlarını **id ile** günceller. Kart ekleyemez, silemez; seviyesini, grubunu ya da sırasını değiştiremez.
2. Aynı değişiklikleri `seed/dictionary_words.sql`'deki satırlara da işle ki yeni kurulan veritabanı doğrudan doğru sözlükle başlasın (yama orada hiçbir kartı değiştirmeden kaydedilir). `TestRepoYamalari_SeedIleUyumlu` seed'in her yamanın son değerini taşıdığını, `TestGercekSeedVeYamalar` ise yamaların gerçek seed'e uygulanabildiğini denetler (CI'da çalışır).
3. `make check` ve entegrasyon testleri (`make ci`), commit, deploy.

Biçim JSON'dur, SQL değil: SQL dosyası istediği her şeyi yapabilirdi; JSON'da "yalnızca id ile bu üç alanı güncelle" sınırını kod zorlar, bilinmeyen id'yi ve yazım hatalı alanı yakalar ve dosya incelemede okunaklı bir fark üretir.

**Nasıl çalışır:**

- Yamalar dosya adındaki sıra numarasına göre uygulanır. Her yama **tek transaction'dır**: kartlar güncellenir ve `dictionary_patches_applied` tablosuna kaydı (`name`, `checksum`, `words`, `updated`, `applied_at`) yazılır; ya hepsi ya hiçbiri.
- Kaydı olan yama **bir daha uygulanmaz**. Uygulanmış bir yamanın dosyasını değiştirme: değişiklik uygulanmaz, açılışta uyarı verilir (checksum farklı). Düzeltme için yeni bir yama ekle.
- Yama idempotenttir: içeriği zaten yeni değerde olan kart yeniden yazılmaz.
- Birden çok replika aynı anda açılırsa advisory lock sayesinde yamayı yalnızca biri uygular; diğerleri kilidi bekler ve yamayı uygulanmış bulur.
- `dictionary_words` boşsa (lokalde seed'siz açılış) yamalar atlanır ve kaydedilmez.
- Tablo, açılıştaki AutoMigrate'le kurulur (yeni ve boş bir tablo; mevcut tablolara dokunulmaz, kilit beklemesi yok).

**Yama başarısız olursa açılış durmaz.** Yama sözlükte olmayan bir id içerirse ya da bir veritabanı hatası olursa:

- O yama bütünüyle geri alınır ve kaydedilmez. Sonraki yamalar da uygulanmaz, çünkü ona dayanıyor olabilirler.
- Logda `hata: sözlük yaması <ad> uygulanamadı, geri alındı (sonraki yamalar da uygulanmadı): yama sözlükte olmayan kelime içeriyor: <id'ler>` satırı çıkar ve hata Sentry'ye gider.
- Sunucu sözlüğün son tutarlı hâliyle açılır: kullanıcılar yalnızca bu düzeltmeleri görmez, başka hiçbir şey bozulmaz.
- Düzeltme: yama dosyasını düzelt (başarısız yama kaydedilmediği için aynı dosya düzeltilebilir), deploy et. Geçici bir veritabanı hatasıysa uygulamayı yeniden başlatmak yeter, yama bir sonraki açılışta yeniden denenir.

Neden durdurmuyoruz: Production'da açılışı durdurmak, Coolify'nin eski container'ı çalıştırmaya devam etmesi demek. Bu da tek bir içerik hatası düzelene kadar acil güvenlik düzeltmeleri dahil **her** deploy'un takılması anlamına gelir. Sözlüğün eski hâli ise tutarlı ve çalışır durumdadır. Bilinmeyen id'nin canlıya ulaşması zaten beklenmez: yamalar CI'da gerçek seed'e karşı uygulanır. Seed yüklenemezse (sözlük boş kalır) açılışın durması kuralı değişmedi.

**Uyarılar** (açılışı durdurmaz; log `uyarı:` satırı + Sentry uyarısı):

- `... "old" ile uyuşmuyordu (elle değiştirilmiş olabilir)`: Kartın canlıdaki içeriği ne yamanın `old`'u ne `new`'i. Kart daha önce elle ya da import aracıyla değiştirilmiş demektir. Yamadaki değer yine yazılır; listedeki id'lere bak.
- `... uygulandıktan sonra değiştirilmiş (checksum farklı)`: Uygulanmış bir yamanın dosyası değişmiş. Değişiklik uygulanmadı; dosyayı geri al ve düzeltmeyi yeni bir yamayla yap.

**Kullanıcı ilerlemesi:** Yama kartın id'sini, seviyesini, grubunu ve sırasını değiştirmez. Bu yüzden `word_progresses` satırları olduğu gibi o karta bağlı kalır: kutu, tarihler ve sayaçlar değişmez. Ürün kararı şu: kartın içeriği düzeltilir, ilerleme o karta ait kalır. İçeriği tamamen değişen bir kartta da böyledir: `A1_4_14`'ü (television → wall) öğrenmiş kullanıcının o kutusunda artık "wall" vardır. İlerleme satırı kartın `english`/`turkish` metninin bir kopyasını taşır (öğrenilen kelimeler ve istatistik listeleri onu gösterir). Yama bu kopyayı aynı transaction'da yeni metne eşitler, logdaki `N ilerleme satırının metni eşitlendi` bu sayıdır. Kopya eşitlenmeseydi kullanıcı bu listelerde kartın eski metnini görürdü.

**Dikkat, import aracı:** `make import-dictionary` Firestore'daki sözlüğü id üzerinden upsert eder ve yamalanmış kartları eski metinlerine döndürür. Yamalar kayıtlı olduğu için kendiliğinden yeniden uygulanmaz. Yamalanmış bir veritabanında import çalıştırma. Çalıştırdıysan `DELETE FROM dictionary_patches_applied;` ile kayıtları sil ve uygulamayı yeniden başlat; yamalar idempotent olduğu için hepsi yeniden uygulanır.

**Geri alma:** Kodu eski bir sürüme döndürmek sözlüğü geri almaz. Eski sürüm `dictionary_patches_applied` tablosunu tanımaz ve yok sayar; düzeltilmiş içerik yerinde kalır. Bir düzeltmeyi geri almak gerekirse `old`/`new`'i ters çevrilmiş yeni bir yama ekle.

**Lokal geliştirme:** Sunucu seed'i ve yamaları varsayılan olarak `/seed`'de arar (`SEED_DIR`); lokalde bu klasör yoksa ikisi de atlanır. Lokal veritabanına yeni yamaları uygulatmak için sunucuyu depo kökünden `SEED_DIR=./seed go run ./cmd/server` ile bir kez başlat (`.env.example`'daki not).

### Acil durum: sözlüğü baştan yüklemek (TRUNCATE)

Yalnızca yamaların çözemeyeceği bir durumda kullan: satırlar eksik ya da bozuk, kart eklemek ya da silmek gerekiyor, seviye/grup/sıra değişiyor. Bu yol kesinti ve elle iş ister.

1. Yeni `seed/dictionary_words.sql` dosyasını üret (lokalde `make import-dictionary`, ardından `dictionary_words` tablosunun `pg_dump --data-only --column-inserts` dökümü, sonra bütün yamaların değerlerini işle), commit + deploy et. **Var olan kartların id'leri aynı kalmalı:** `word_progresses` kartlara id ile bağlıdır, sözlükte karşılığı kalmayan ilerleme satırı kullanıcıda görünmez olur.
2. PostgreSQL kaynağının terminalinde tabloyu boşalt: `psql -U postgres -d <veritabanı-adı> -c "TRUNCATE dictionary_words;"`
3. Application'ı yeniden başlat; boş tabloyu görünce yeni seed'i yükler. Kayıtlı yamalar yeniden uygulanmaz; seed onların değerlerini zaten taşımalıdır (`TestRepoYamalari_SeedIleUyumlu`).

2. ve 3. adım arasında uygulama kelime gösteremez; ikisini art arda ve sakin bir saatte yap. Tabloyu boşaltıp yeniden başlatmayı unutma: seed yalnızca açılışta ve tablo boşken yüklenir.

---

## 7) Flutter'ı production'a yönlendir

Uygulama build alırken backend adresini ver:

```bash
flutter build apk --release --dart-define=API_BASE_URL=https://<coolify-domain>/api/v1
# iOS:
flutter build ipa --release --dart-define=API_BASE_URL=https://<coolify-domain>/api/v1
```

---

## Sentry rölesi

**Neden:** Türk Telekom (TTNet) Sentry'nin alım adreslerini (`*.ingest.us.sentry.io`, `*.ingest.de.sentry.io`) DNS/SNI düzeyinde engelliyor (adresler `195.175.254.2`'ye çözülüyor). Bu hatlardaki kullanıcıların çökme raporları Sentry'ye hiç ulaşmıyor. İstanbul'daki sunucumuz Sentry'ye ulaşabildiği için uygulama raporlarını kendi API'mize gönderir, sunucu da onları olduğu gibi Sentry'ye aktarır.

**Nasıl çalışır:** Sentry SDK'sı gönderim adresini DSN'den kurar: `https://ANAHTAR@HOST/yol/PROJE_NO` için `https://HOST/yol/api/PROJE_NO/envelope/`. Mobil uygulama DSN'i API adresimizi gösterecek biçimde derlenir; istekler `POST /api/v1/sentry/api/<PROJE_NO>/envelope/` ucuna gelir. Sunucu:

- yalnızca yol'daki proje numarası ve `X-Sentry-Auth` başlığındaki (ya da sorgudaki) `sentry_key`, `SENTRY_RELAY_DSN`'deki proje ve açık anahtarla aynıysa aktarır; aksi halde `403`,
- gövdeyi açmadan, değiştirmeden ve loglamadan `https://<sentry-host>/api/<PROJE_NO>/envelope/` adresine gönderir; yalnızca `X-Sentry-Auth`, `Content-Type`, `Content-Encoding` ve `User-Agent` başlıkları gider. İstemcinin IP'si (`X-Forwarded-For` vb.) ve çerezler **gitmez**: Sentry isteği sunucudan gelmiş görür,
- Sentry'nin durum kodunu (2xx/4xx/5xx; `Retry-After` ve `X-Sentry-Rate-Limits` başlıklarıyla) olduğu gibi döner; Sentry'ye 10 sn içinde ulaşılamazsa `502`,
- 1 MiB'tan büyük gövdeyi `413` ile reddeder; `SENTRY_RELAY_DSN` boşsa `404` döner.

Uç kimliksizdir, zorunlu güncelleme (426) denetiminden muaftır (eski sürümün raporları da gelmeli) ve genel hız sınırına (`API_RATE_LIMIT`, IP başına) tabidir.

**Ortam değişkeni:** `SENTRY_RELAY_DSN` = mobil projenin Sentry'deki gerçek DSN'i (Sentry → Project Settings → Client Keys). Geçersizse (https değil, anahtar ya da sayısal proje numarası yok) sunucu yine açılır, açılış loguna `config: uyarı: SENTRY_RELAY_DSN geçersiz ...` yazılır ve röle kapalı kalır. Sunucunun kendi hatalarını raporladığı `SENTRY_DSN`'den bağımsızdır.

**Mobil derleme:** Uygulama, gerçek DSN yerine **röle DSN'i** ile derlenmelidir; anahtar ve proje numarası gerçek DSN'dekiyle aynıdır, yalnızca host ve yol değişir:

```
gerçek:  https://ANAHTAR@o4512437762981888.ingest.us.sentry.io/PROJE_NO
röle:    https://ANAHTAR@<api-host>/api/v1/sentry/PROJE_NO
```

**Doğrulama:** deploy logunda `sentry rölesi: açık (proje <PROJE_NO> → <sentry-host>)` satırı görünmeli (`kapalı` yazıyorsa değişken boş ya da geçersizdir; geçersizse açılışın başındaki `config: uyarı: SENTRY_RELAY_DSN geçersiz ...` satırı nedenini söyler). Ardından (boş bir envelope Sentry'de olay oluşturmaz):

```bash
# Sentry'nin kendi yanıtı beklenir (genelde 200; boş envelope'a Sentry 4xx de dönebilir, o da
# rölenin Sentry'ye ulaştığını gösterir). Rölenin kendi yanıtları: 404 {"code":"not_found"} =
# SENTRY_RELAY_DSN boş/geçersiz; 403 {"code":"forbidden"} = anahtar ya da proje uyuşmuyor;
# gövdesiz 502 = sunucu Sentry'ye ulaşamadı.
curl -s -w '\n%{http_code}\n' -X POST "https://<api-host>/api/v1/sentry/api/<PROJE_NO>/envelope/" \
  -H 'Content-Type: application/x-sentry-envelope' \
  -H 'X-Sentry-Auth: Sentry sentry_version=7, sentry_client=curl/1.0, sentry_key=<ANAHTAR>' \
  --data-binary $'{}\n'
# Yanlış anahtar 403 beklenir (Sentry'ye gitmez):
curl -s -o /dev/null -w '%{http_code}\n' -X POST "https://<api-host>/api/v1/sentry/api/<PROJE_NO>/envelope/" \
  -H 'X-Sentry-Auth: Sentry sentry_key=yanlis' --data-binary $'{}\n'
```

Uçtan uca kontrol: röle DSN'iyle derlenmiş uygulamada test hatası üret (Türk Telekom hattında, Wi-Fi kapalı) ve olayın birkaç saniye içinde Sentry'de göründüğünü gör.

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

Bu bölüm, `6b85321`'den sonraki backend sprint 1 değişikliklerini canlıya almak içindir. API değişiklikleri geriye dönük uyumludur: yeni istek alanları isteğe bağlıdır, yanıtlara yalnızca alan eklenmiştir ve yayındaki mobil istemci hiçbir şey göndermeden çalışır. Tek bilinçli fark "kaldığın grup" işaretçisindedir: yayındaki istemcide işaretçi artık yalnızca gösterdiği grup gerçekten tamamlanınca ilerler (aşağıda `POST /users/me/advance-group` maddesi). Eski sunucu tur sonunda işaretçiyi koşulsuz ilerletiyordu.

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

- Kolonlar boş bırakılabilir (nullable) ve varsayılansızdır: ekleme anlıktır, veri taşıması yoktur. Index ise `CONCURRENTLY` olmadan kurulur: kurulum boyunca `refresh_tokens`'a yazma (bütün `/auth/refresh` ve sosyal giriş istekleri, rolling deploy'da eski container'dakiler de) bekler. Migration `statement_timeout` kapalı çalıştığı için hata vermez, yalnızca bekletir; bekleme tablo boyutuyla büyür. Eski sunucu bu tabloyu hiç temizlemedi ve her yenileme bir satır ekledi, yani tablo küçük **olmayabilir**.
  - **Deploy öncesi say:** `SELECT count(*) FROM refresh_tokens;`
  - Sayı büyükse (yüz binler) iki seçenek var: (a) aşağıdaki parçalı silmeyle tabloyu önce küçült; ya da (b) kolonları ve index'i deploy'dan önce elle, aynı adla kur. AutoMigrate var olan kolonu ve index'i görüp yeniden kurmaz (`TestMigrate_OnceElleKurulanKolonVeIndexKorunur`):
    ```sql
    ALTER TABLE refresh_tokens ADD COLUMN IF NOT EXISTS family_id varchar(32), ADD COLUMN IF NOT EXISTS replaced_by_id bigint;
    CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_refresh_tokens_family_id ON refresh_tokens (family_id);
    ```
    `CREATE INDEX CONCURRENTLY` transaction içinde çalışmaz, ayrı bir komut olarak ver. Yarıda kalırsa index `INVALID` kalır: `DROP INDEX CONCURRENTLY idx_refresh_tokens_family_id;` ile silip yeniden dene (AutoMigrate geçersiz index'i de "var" sayar). Eski sunucu bu kolonları yok sayar.
- Migration tek bağlantıda, `statement_timeout` kapalıyken ve bağlantı ömürlü bir advisory lock altında çalışır: aynı anda açılan birden çok replika (ya da CI'da paralel test paketleri) sırayla migrate eder, biri "already exists" ile düşmez. Başarısız olursa yeni container hiç ayağa kalkmaz, Coolify eskisini çalıştırmaya devam eder.
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
- **`POST /users/me/advance-group`:** İsteğe bağlı gövde `{"level","group"}` (tamamlanan grup) ile idempotenttir: işaretçi yalnızca o gruptaysa bir ileri gider (yeni istemci; sonradan eklenen koşullar için "Grup açma kuralı" bölümündeki tabloya bak). C2'nin son grubundan sonra A1 Grup 1'e geri sarma olmaz.
  - **Gövdesiz istek (yayındaki istemci):** İşaretçi yalnızca gösterdiği grup haritada **tamamlanmışsa** (grubun hiçbir kelimesi kutu 1'de ya da kayıtsız değil; haritadaki "tamamlandı" ile aynı kural) bir ileri gider. Değilse hiçbir şey değişmez ve yanıt aynı şekilde (`user`, `levelChanged:false`, `newLevel`, `newGroup` = mevcut işaretçi) 200 döner. Seviyenin son grubunda sonraki seviyeye yalnızca o grup tamamlanınca geçilir.
  - **Kullanıcının göreceği fark:** Eski istemci tur sonunda, "Bilmiyorum" denen kelimeler kutu 1'de kalsa da advance çağırır. Eski sunucu işaretçiyi her seferinde ilerletiyordu; yeni sunucuda işaretçi yarım kalan grupta kalır ve ana sayfadaki "Öğrenmeye Başla" kalan kelimelere götürür. Geride bir grubu tekrarlamak (current-group geri yazmaz) işaretçiyi artık kaydırmaz; sahte "yeni seviye" diyaloğu ve kilitli bir sonraki seviyeye yönlendirme olmaz. Eski sunucunun önceden ileri kaydırdığı işaretçi olduğu yerde kalır, geri alınmaz.
  - **Kalan eski davranış:** Kullanıcı önceki bir seviyedeki grubu tekrarlarken (işaretçi sonraki seviyede) tur bitince istemci, işaretçinin seviyesi tekrarlanan gruptan farklı olduğu için "yeni seviye" diyaloğunu gösterir. Bu eski sunucuda da böyleydi, istemci tarafında düzelir.
  - Eski istemci son kartın kaydını beklemeden advance'i de gönderir. Sunucu, grup henüz tamamlanmamış görünürse kararı en fazla 1 sn yeniden sorar. İşaretçideki grup tur sonunda tamamlanmamışsa (Bilmiyorum'lu yarım tur ya da işaretçinin gerisindeki bir grubun tekrarı) tebrik ekranı bu yüzden yaklaşık 1 sn gecikir. Gövdeli istek (yeni istemci) beklemez.
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
  - **Günde bir ücretsiz grup:** `POST /users/me/usage/group-started` (gövdeli) bugün zaten ücretsiz bir grup açılmışken başka bir grup için gelirse ihlaldir (`reason=free_group_reused`): `off`'ta kabul, `log`'da kabul + rapor, `enforce`'ta `403 gate_required` ve hiçbir şey yazılmaz. Aynı gruba yeniden istek (yanıtı kaybolan deneme) ihlal değildir. Yayındaki istemci bu ucu yalnızca `groupsStartedTodayCount == 0` iken çağırır; dürüst kullanımda yalnızca iki cihazdan aynı anda farklı gruba ilk girişte görülebilir.
  - **İhlal kaydı:** Her ihlal tek satırdır: `gate_violation kind=<learn|review> reason=<neden> mode=<kip> user=<id> request_id=<id> app_version=<sürüm> ...`. `app_version`, istemcinin isteğe bağlı `X-App-Version` başlığıdır (ör. `1.0.3+57`); başlık yoksa `eski` (yayındaki istemci göndermez), geçersizse `gecersiz`. Sentry olayı aynı bilgiyi `gate_kind`, `gate_reason`, `gate_mode`, `app_version` etiketleriyle taşır.
  - **Nedenler:** `group_not_unlocked` (grup bugün açılmadı), `map_locked` (grup haritada kilitli), `free_group_reused` (günün ücretsiz hakkı zaten kullanıldı), `review_checkpoint` (tekrar kapısı geçilmedi), `rollover_expired` (grup satırın son yazıldığı geçmiş günde açılmıştı ve gece yarısından sonraki 1 saatlik tolerans bitti: öğrenme sayfası gece boyunca açık kalan dürüst istemci de bunu üretir), `open_group_limit` (3 açık grup varken başlanmamış bir grup açılmak istendi; bu nedenin ret kodu `gate_required` değil `403 open_group_limit`'tir, bkz. "Grup açma kuralı" bölümü).
  - **Sınır:** Reklam kayıtları (`group-unlocked-by-ad`, `review-ad-pass`) sunucuda doğrulanamaz (AdMob sunucu tarafı doğrulaması, SSV, yok). `enforce` yalnızca kaydı hiç yapmayan ya da ücretsiz hakkı yeniden kullanan istemcileri durdurur; reklam izlemeden kayıt çağrısını yapan değiştirilmiş bir istemciyi durduramaz.

### Güvenli yayın sırası

1. **Hazırlık:** CI'da son commit yeşil (`test` ve `docker` işleri). Ortam değişkenleri yukarıdaki tabloya göre kontrol edildi; `GATE_ENFORCEMENT` açıkça `log`.
   - **Coolify → Application → Health Check:** arayüz health check'i **kapalı** olmalı. Eski rehberdeki `/api/v1/health` yolu (Health Check Path) tanımlıysa kaldır. Açık kalırsa Coolify'nin probe'u imajdaki `HEALTHCHECK`'in yerine geçer; distroless imajda `curl`/`wget` olmadığı için yeni container hiç `healthy` olmaz ve deploy geri alınır ya da `unhealthy` kalır (3. bölümdeki nota bak). İlk deploy'da `docker inspect --format '{{.State.Health.Status}}' <container>` ile `healthy` olduğunu doğrula.
   - **`refresh_tokens` boyutu:** `SELECT count(*) FROM refresh_tokens;` ve silinecek satır sayısı ("Açılışta AutoMigrate'in uygulayacağı veritabanı değişiklikleri" bölümü). Büyükse index'i önceden kur ya da tabloyu önce parçalı silmeyle küçült.
2. **Deploy öncesi yedek:** PostgreSQL kaynağı → Backups → **Backup Now**, satır "Success" olmalı. Migration geri alınabilir bir değişiklik olsa da sprint sonrası ilk deploy öncesinde elde taze bir dump bulunsun.
3. **Önce backend:** Backend'i deploy et. Yayındaki istemci için güvenlidir: yeni alanları göndermese de çalışır. Tek fark, işaretçinin yalnızca grup tamamlanınca ilerlemesidir (Davranış değişiklikleri → `advance-group`). Mobil istemcinin yeni sürümü (advance-group gövdesi, `?limit=50`, Apple silme kodu, kapı kodlarının işlenmesi) backend canlıdayken yayınlanmalıdır, **önce değil**. Yeni istemci eski backend'e karşı da çalışır ama sunucu tarafı korumalardan yararlanamaz.
4. **İzle (en az birkaç gün):** "Yayın sonrası doğrulama" listesini ve aşağıdaki canlı kontrolleri uygula. Sentry'de şunlara bak: yeni 5xx türleri (özellikle sorgu zaman aşımı, `canceling statement due to statement timeout`), kapı ihlali bilgileri (`log` kipinde), `!!! UYARI !!!` satırı ya da `refresh token temizliği başarısız`.
5. **Sonra enforce:** `X-App-Version` başlığını gönderen ilk mobil sürüme X diyelim (başlık istemciye henüz eklenmedi; eklendiği sürüm X'tir). Ölçüt: `log` kipinde birkaç gün boyunca **X ve sonraki sürümlerden `rollover_expired` dışında ihlal yok**. Kontrol: `docker logs <container> 2>&1 | grep gate_violation | grep -v 'app_version=eski' | grep -v 'reason=rollover_expired'` çıktısında X'ten eski olmayan bir sürüm kalmamalı. Sentry'de aynı süzme `app_version` ve `gate_reason` etiketleriyle yapılır. `app_version=eski` satırları eski istemcidendir ve bu ölçüte girmez: eski istemci başka bir cihazdan verilen tekrar cevaplarını kapı zamanına kadar görmediği için dürüst kullanımda da `review_checkpoint` üretebilir. Ölçüt sağlanınca ve yeni istemci yaygınlaşınca `GATE_ENFORCEMENT=enforce` yapıp uygulamayı yeniden başlat (redeploy gerekmez). Eski istemci `gate_required` / `review_gate_required` kodlarını tanımaz, ham hata gösterir. Eski sürümün payı hâlâ yüksekse enforce'a geçme ya da önce zorunlu güncelleme kararını ver.

### Geri alma (rollback)

- **Yalnızca kapı sorunu:** `GATE_ENFORCEMENT=log` (ya da `off`) yapıp yeniden başlat. Kod geri alınmaz.
- **Sorgu zaman aşımı kaynaklı 500'ler:** `DB_STATEMENT_TIMEOUT`'u artır ya da `0` yap, yeniden başlat.
- **Kodu geri almak:** Coolify'de bir önceki commit'i deploy et. Şema geri alma gerekmez: yeni kolonlar boş bırakılabilir, eski kod onları yok sayar (AutoMigrate kolon silmez). Eski sunucu yeni access token'ları kabul eder (`iss`/`aud`'a bakmaz), refresh token'lar geçerli kalır, kullanıcılar çıkış yapmaz. Geri alınca refresh toleransı, aile iptali ve saatlik temizlik de geri gider; oturumlar eski kurallarla sürer.
- **Geri alırken dikkat:** Eski kod `APP_ENV` boşken `development` sayar; değişkeni hiçbir zaman kaldırma (production'a özgü korumalar, ör. sayaç sıfırlama ucunun kapanması, devreden çıkar). Geri alınan sürüm Leitner'in eski yanlış cevap kuralına (üç yanlışta bir alt kutu) döner; aradaki günlerde yazılmış kutu/tarihler olduğu gibi kalır, düzeltme gerekmez.
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

---

## E-posta/parola girişi ve misafir hesabı: sürüm notları ve deploy kontrol listesi

Bu bölüm, backend sprint 1'in üstüne gelen e-posta/parola girişi ve misafir hesabı değişikliklerini canlıya almak içindir. Yayındaki mobil istemci için geriye dönük uyumludur: kullanıcı yanıtlarına yalnızca `isGuest` alanı eklendi, yayındaki istemci yeni uçları çağırmadan eskisi gibi çalışır.

### Yeni ve değişen uçlar

| Uç | Ne değişti |
|---|---|
| `POST /auth/register`, `POST /auth/login` | Production'da da açık (eskiden 404). Hata kodları aynı: `400 invalid_request / invalid_email / weak_password / invalid_display_name`, `409 email_in_use`, `401 invalid_credentials`. `@users.kelimeler.local` adresleri `invalid_email` |
| `POST /auth/email/lookup` | Yeni. Yetkisiz, `AUTH_RATE_LIMIT`'e tabi (giriş/kayıtla aynı kova). Gövde `{"email"}`; kayıt ve girişle aynı normalleştirmeyle (küçük harf, kenar boşlukları atılmış) aranır. `200 {"next":"login"}`: adres parola kimliği olan bir hesaba ait (yanında Google/Apple olsa da). `200 {"next":"register"}`: adres kayıtlı değil. `200 {"next":"social","providers":[...]}`: adres var ama parola kimliği yok; `providers` bağlı sağlayıcılar (`"apple"`, `"google"`, ada göre sıralı; kimliksiz hesapta `[]`), bu adresle kayıt `409 email_in_use` alır. Hatalar: `400 invalid_request` (bozuk JSON), `400 invalid_email` (geçersiz ya da `@users.kelimeler.local` yer tutucu adres: misafir ve e-postasız sosyal hesaplar hiçbir zaman eşleşmez), `429 rate_limited`, veritabanı hatasında `500` (asla "register" diye yutulmaz). Adresin kayıtlı olup olmadığını **bilerek** açığa vurur: aşağıda "Davranış ve izleme" |
| `POST /auth/guest` | Yeni. Yetkisiz, `AUTH_RATE_LIMIT`'e tabi. İsteğe bağlı `{"displayName"}`; `201` + `AuthResponse`. E-postasız misafir hesabı açar (`users.is_guest=true`, adres `guest_<id>@users.kelimeler.local`, giriş kimliği yok) |
| `POST /auth/link/password` | Yeni. Token ister, `AUTH_RATE_LIMIT`'e tabi. Yalnızca misafirde (aksi `409 not_guest`); `{"email","password"}` ile hesabı kaydeder, `200 {"user": ...}` |
| `POST /auth/link/google`, `/auth/link/apple` | Misafirde hesabı kaydeder (`isGuest=false`); sağlayıcının doğruladığı adres başka hesapta değilse kullanıcının e-postası olur |
| Sosyal giriş (`/auth/google`, `/auth/apple`) | Doğrulanmış e-posta, tek giriş yolu parola olan hesabı devralır; parolanın yanında başka giriş yolu olan ve adresi kanıtlamamış hesaptan adresi geri alır, sahibine yeni hesap açar (eskiden `409 email_in_use`; bkz. 4. bölüm, "Giriş yolları ve e-postayla hesap eşleme" ve "Adresin geri alınması") |
| Bütün kullanıcı yanıtları | `user.isGuest` eklendi; yer tutucu adresler (misafir ve e-postasız Apple/Google hesapları) `email: ""` döner. **Eskiden** e-postasız Apple hesaplarında `apple_<sub>@users.kelimeler.local` dönüyordu; yayındaki istemcinin profilinde bu satır artık boş görünür |
| `DELETE /auth/link/google`, `/auth/link/apple` | `password` kimliği her ortamda geçerli giriş yolu sayılır (son giriş yolu kuralı); eskiden production'da sayılmıyordu |

### Ortam değişkeni

| Değişken | Ne değişti | Deploy öncesi yapılacak |
|---|---|---|
| `GUEST_REFRESH_TOKEN_TTL` | Yeni, varsayılan `8760h` (1 yıl). Sıfır ya da negatif değerde sunucu açılmaz | Boş bırakılabilir (varsayılan uygulanır). Kısaltırsan, o süre uygulamayı açmayan misafirin hesabına bir daha ulaşılamayacağını hesaba kat |

### Açılışta AutoMigrate'in uygulayacağı veritabanı değişikliği

| Tablo | Değişiklik | Not |
|---|---|---|
| `users` | `is_guest boolean NOT NULL DEFAULT false` | Var olan bütün hesaplar `false` olur. PostgreSQL 11+ sabit varsayılanlı kolonu tabloyu yeniden yazmadan ekler: işlem anlıktır, yalnızca kısa bir tablo kilidi alır (kilit, `users`'ta süren uzun bir sorguyu bekleyebilir) |
| `users` | `token_version bigint NOT NULL DEFAULT 0` | Var olan bütün hesaplar `0` olur; `is_guest` gibi anlıktır. Devir ve geri almada artar; hesabı denetleyen uçlar eski sürümlü access token'ı reddeder (4. bölüm, "Devirden ve geri almadan önce verilmiş access token'lar") |
| `auth_identities` | `email_verified boolean NOT NULL DEFAULT false` | Var olan bütün kimlikler (Google/Apple dahil) `false` olur; gerekçe ve isteğe bağlı elle işaretleme: 4. bölüm, "Adresin geri alınması". Ekleme `is_guest` gibi anlıktır |

Index eklenmez. Eski sunucu kolonları yok sayar; geri almada kolonlar kalabilir.

### Davranış ve izleme

- **Devir logu:** `auth: doğrulanmış <google|apple> e-postası parola hesabını devraldı; parola kimliği silindi, oturumlar iptal edildi (user=<id>)`. Destek talebinde "parolamla giremiyorum" diyen kullanıcı için bu satıra bak: parola, adresin doğrulanmış sahibi Google/Apple ile girdiği için kaldırılmıştır; kullanıcı o sağlayıcıyla girer.
- **Geri alma logu:** `auth: doğrulanmış <google|apple> e-postası adresi parolayla tutan hesaptan geri aldı; eski hesabın parola kimliği silindi, adresi yer tutucuya çevrildi, oturumları iptal edildi (eski=<id>, yeni=<id>)`. Destek talebinde "parolam çalışmıyor, e-postam görünmüyor" diyen kullanıcı için bu satıra bak: adresin doğrulanmış sahibi Google/Apple ile girdi; kullanıcı eski hesabına (verisi yerinde) bağlı kalan Google/Apple ile girer. İki hesap birleştirilmez.
- **Dönüşüm logu:** `auth: misafir hesabı <google|apple> ile kaydedildi (user=<id>)`.
- **Parola sıfırlama yoktur.** Parolasını unutan kullanıcı ancak hesaba Google/Apple bağladıysa onlarla girer.
- **Giriş yanıtları adres ele vermez:** bilinmeyen adres, parolasız hesap ve yanlış parola aynı `401 invalid_credentials` gövdesini ve yaklaşık aynı süreyi (bir bcrypt karşılaştırması) alır. Kayıt ise bilerek `409 email_in_use` döner (sözleşme); kayıtlı adresler kayıt ucundan öğrenilebilir, sıkı hız sınırı bunu yavaşlatır.
- **E-posta sorgusu adresi bilerek ele verir (ürün kararı, 27 Ekim):** `POST /auth/email/lookup`, e-posta önce giriş ekranının kullanıcıyı parola ya da kayıt sayfasına göndermesi için adresin kayıtlı olup olmadığını ve parolası mı yoksa yalnızca Google/Apple'ı mı olduğunu söyler. Bu akışın doğasıdır (Google ve Microsoft girişleri de böyle çalışır); kayıtlı olup olmadığı zaten kayıt ucunun `409`'undan öğrenilebiliyordu, yeni olan yalnızca hesabın giriş yöntemi (parola / Google / Apple) ve sorgunun hesap açmadan yapılabilmesidir. Toplu adres taramasını IP başına sıkı sınır (`AUTH_RATE_LIMIT`, varsayılan 10/dk, giriş ve kayıtla **aynı kova**) yavaşlatır: sorgu taramaya ayrı bir bütçe açmaz. Giriş ucunun birleşik `401 invalid_credentials` davranışı değişmedi. Yer tutucu adresler (`@users.kelimeler.local`) `400 invalid_email` alır; misafir ve e-postasız sosyal hesaplar sorguyla bulunamaz. Taramadan şüphelenirsen (tek IP'den sürekli `429 rate_limited`), `AUTH_RATE_LIMIT`'i düşürmek giriş/kayıt denemelerini de aynı ölçüde kısar.
- **Misafir hesapları birikir:** her `/auth/guest` çağrısı bir satırdır. Aşağıdaki temizliği birkaç ayda bir çalıştır.
- **Yayın öncesi parola hesabı denetimi** (4. bölümdeki eski not) artık gerekmez: parola hesapları meşrudur, başkasının adresiyle açılmış olanı adresin sahibi Google/Apple ile girince devralır.

### Yayın sırası

1. Backend'i deploy et (yukarıdaki "Güvenli yayın sırası"ndaki yedek ve izleme adımları geçerli). Açılış logunda `db: migrate tamamlandı`.
2. "Yayın sonrası doğrulama" 5. madde: gövdesiz `POST /api/v1/auth/register` → `400`. E-posta önce giriş ekranlı istemci için gövdesiz `POST /api/v1/auth/email/lookup` de `400` dönmeli (`404` ise canlıda sorgu ucundan eski bir derleme çalışıyordur; bu istek de `AUTH_RATE_LIMIT` kovasından bir hak harcar).
3. E-posta ve misafir ekranlı mobil sürümü backend canlıdayken yayınla, **önce değil**: eski backend'de `/auth/guest`, `/auth/link/password` ve `/auth/email/lookup` 404 döner (sorgu 404 dönerse e-posta önce giriş ekranı "Devam et"te ilerleyemez).

### Geri alma

Bir önceki commit'i deploy etmek şemayı bozmaz (`is_guest` kalır, eski kod yok sayar), ama yeni mobil sürüm yayındaysa:
- `/auth/guest`, `/auth/link/password`, `/auth/email/lookup` ve production'da `/auth/register`, `/auth/login` 404 döner: yeni istemcinin e-posta ve misafir butonları çalışmaz, e-posta/parola ile açılmış hesaplara girilemez (Google/Apple bağlanmışsa onlarla girilir).
- Var olan misafirler oturumlarını korur (refresh token'ın bitiş tarihi satırda yazılıdır), ama eski sunucu rotasyonda 30 günlük ömür verir: 30 gün uygulamayı açmayan misafir hesabına ulaşamaz.
- Eski sunucu doğrulanmış e-postayla gelen Google/Apple girişini, parolalı hesapla eşleşince `409 email_in_use` ile reddeder (devir de geri alma da yapmaz).
- Eski sunucu `tv` claim'ine ve `users.token_version`'a bakmaz: geri dönülürse devirden önce verilmiş access token'lar süreleri dolana kadar hesabı denetleyen uçlarda da kabul edilir.
- Adresi geri alınmış hesaplar `reclaimed_<id>@users.kelimeler.local` adresini korur. Yer tutucuyu gizleme kuralından (`email: ""`) eski bir sürüme dönülürse bu adres istemcide görünür.

### Terk edilmiş misafir temizliği

`cmd/cleanup-guests` zamanlanmış değildir, elle çalıştırılır. Aday: **hâlâ misafir**, **hiçbir giriş kimliği olmayan** ve son etkinliği `--inactive-days` (varsayılan `180`, en az `30`) günden eski hesap. Son etkinlik şu zaman damgalarının en yenisidir:

- `refresh_tokens.created_at` (asıl ölçüt): uygulama her açılışta access token'ı yeniler ve her rotasyon yeni bir satır yazar. Misafirin token ömrü (`GUEST_REFRESH_TOKEN_TTL`, 1 yıl) eşikten uzun olduğu için son satır saatlik token temizliğinden sağ çıkar; satır kalmamışsa token da kalmamıştır, hesaba zaten ulaşılamaz.
- `word_progresses.updated_at`, `daily_logins.created_at` (öğrenme/tekrar, günlük giriş), `users.updated_at` ve `users.created_at`.

Koşul, silmeden hemen önce kullanıcı satırı kilitliyken yeniden sınanır: liste alındıktan sonra uygulamayı açan ya da hesabını kaydeden misafir silinmez. Kelime ilerlemesi, token'lar, girişler ve rozetler CASCADE ile silinir; yedeklerdeki kopyalar yedek saklama süresi sonunda gider (hesap silme sayfasındaki söz gibi).

Production'da (sunucuda, SSH ile; imaj distroless olduğu için kabuk yok, binary doğrudan çalıştırılır ve container'ın ortamını kullanır):

```bash
docker ps --filter "name=<uygulama-adı>" --format '{{.Names}}'
# Önce yalnızca rapor (varsayılan): aday sayısı ve ilk 20 aday
docker exec <container> /cleanup-guests
# Sonra sil
docker exec <container> /cleanup-guests --apply
# Farklı eşik
docker exec <container> /cleanup-guests --inactive-days 365 --apply
```

Lokalde: `make cleanup-guests` (rapor) ve `make cleanup-guests ARGS="--apply"`. Çıktının sonunda `silindi: N, atlandı (bu arada etkinleşti): M, hata: K` yazar; hata varsa çıkış kodu 1'dir ve satırlar `HATA <id>: ...` ile listelenir. Çalıştırmadan önce taze bir yedek al (PostgreSQL → Backups → Backup Now): silme geri alınamaz.

---

## Grup açma kuralı ("görüldü") ve açık grup sınırı: sürüm notları

Ürün kararı: kullanıcı bir grubun bütün kelimelerini bir kez cevapladıysa ("Biliyorum" ya da "Bilmiyorum") seviyedeki sonraki grup açılır; bilmediği kelimeler kalsa da beklemez. Aynı anda en fazla **3** grup açık kalabilir (`services.MaxOpenGroups`). Yayındaki mobil istemci için geriye dönük uyumludur: yanıtlara yalnızca alan eklendi, eski alanların anlamı değişmedi, eski uçlar çalışır.

### Tanımlar

- **Görüldü:** grubun bütün kelimelerinin ilerleme kaydı var (kutu fark etmez; "Bilmiyorum" denen yeni kelime kutu 1'de kaydedilir, bkz. `POST /progress/unknown`).
- **Tamamlandı (değişmedi):** grupta kaydı olmayan ya da kutu 1'de kelime kalmadı. Tamamlanan grup görülmüştür de.
- **Açık grup:** başlanmış (en az bir kayıt) ama tamamlanmamış grup; bütün seviyeler birlikte sayılır. Önceki seviyesi tamamlanmamış seviyenin grupları (kendileri `previous_level` ile kilitli) başlanmış olsa da sayılmaz ve `GET /map/open-groups` listesinde yer almaz. Neden: Firestore'dan taşınan kayıtlar ya da sözlüğe sonradan eklenen bir grup (önceki seviyeyi yeniden "tamamlanmamış" yapar) kilitli grupları sınıra doldurup kullanıcıyı büsbütün takabilirdi. Önceki seviye tamamlanınca bu gruplar açılır ve yeniden sayılır; sınırı aşmış olabilirler, yeni grup yine yer açılana kadar açılmaz.
- **Kilit:** önceki seviye tamamlanmadıysa seviyenin bütün grupları kilitli (değişmedi). Başlanmış grup her zaman açık. Başlanmamış grup, seviyedeki önceki grup **görüldüyse** (eskiden: tamamlandıysa) ve açık grup sayısı 3'ten azsa açık. Birden çok neden varsa öncelik: `previous_level`, `previous_not_seen`, `open_group_limit`. Önceki seviyesi tamamlanmamış gruba sınır hiç uygulanmaz (başlasa da sayılmaz); ret nedeni `previous_level`'dır (learn'de `map_locked`, `403 gate_required`).
- **Seviye tamamlama ve rozetler değişmedi:** seviye = bütün grupları tamamlandı.

### Yeni ve değişen uçlar

| Uç | Ne değişti |
|---|---|
| `GET /map/levels/:level/groups` | Her gruba `isSeen` (bool), `seenWords` (kaydı olan kelime sayısı) ve `lockReason` eklendi: `""` (açık), `"previous_level"`, `"previous_not_seen"`, `"open_group_limit"`. Eski alanlar (`isUnlocked`, `isCompleted`, `hasStarted`, `remainingInBox1`, `totalWords`) aynı anlamda. Önceki grubu görülmüş ama tamamlanmamış başlanmamış grup artık `isUnlocked: true` |
| `GET /map/open-groups` | Yeni. `200 {"openGroupCount":2,"maxOpenGroups":3,"openGroups":[{"level":"A1","group":2,"totalWords":50,"remainingInBox1":7}]}`; yalnızca sınıra sayılan gruplar (önceki seviyesi tamamlanmamış gruplar yok), `openGroupCount` listenin uzunluğu; liste seviye ve grup sırasında, boşsa `[]` |
| `POST /progress/unknown` | Yeni. Gövde `{"wordId"}`: kaydı olmayan kelimeye "Bilmiyorum". Kelimeyi kutu 1'de kaydeder (`wrongCount: 1`), yanıt learn'ünkiyle aynı biçimde kelime ilerlemesi. Kaydı olan kelimede hiçbir şey değiştirmez, mevcut kaydı döner (idempotent; kutu 1'e geri atmaz). Learn ile aynı kapıdan geçer. Hatalar: `400 invalid_request`, `404 unknown_word`, `403 gate_required`, `403 open_group_limit` |
| `POST /progress/learn` | Sınır doluyken başlanmamış grubun ilk kelimesi ihlaldir (`reason=open_group_limit`); `enforce`'ta `403 open_group_limit` |
| `POST /progress/swipe` | Kutu 1'deki kelimeye sağ (öğrenme) artık learn gibi günlük grup kapısından geçer (`enforce`'ta `403 gate_required`). Kutu 1 kaydı "Bilmiyorum" ile yazılabildiği için gerekli: aksi halde grup bir kez açılıp sonraki günlerde reklamsız öğrenilirdi. Kutu 1 sol ve tekrar cevapları değişmedi |
| `POST /users/me/usage/group-started`, `/group-unlocked-by-ad` (gövdeli) | Sınır doluyken başlanmamış grup için ihlal (`reason=open_group_limit`, ücretsiz hak ihlalinden önce gelir); `enforce`'ta `403 open_group_limit` ve hiçbir sayaç yazılmaz (hak ya da reklam sayılmaz). Gövdesiz istek değişmedi |
| `PUT /users/me/current-group` | İleri yazım, sınır doluyken başlanmamış gruba `403 open_group_limit` döner ve hiçbir alan değişmez (kipten bağımsız; işaretçi kilitli gruba konmaz). Geri ve aynı gruba yazım değişmedi |
| `POST /users/me/advance-group` | Yanıta `blockedReason` eklendi: `""` (ilerledi ya da aşağıdaki nedenler dışında değişmedi: gövde işaretçiyle uyuşmadı, işaretçi "tümü tamamlandı"da ya da gövdesiz istekte grup tamamlanmadı), `"previous_not_seen"`, `"previous_level"`, `"open_group_limit"`. Gövdeli istek (`{"level","group"}`) artık grup görüldüğünde de gönderilebilir; işaretçi o gruptaysa ve grup **sunucuda görülmüşse** ilerler. Görülmemişse işaretçi yerinde kalır, `blockedReason: "previous_not_seen"` (istemcinin sözüne güvenilmez). Seviyenin son grubundan sonraki seviyeye (C2'de "tümü tamamlandı"ya, `C2`/grup `0`) yalnızca seviye **tamamlanmışsa** (ya da seviye rozeti varsa; haritadaki `previous_level` kilidiyle aynı kural) geçilir; değilse işaretçi son grupta kalır, `levelChanged: false`, `blockedReason: "previous_level"`. `TEST_MODE_UNLOCK_ALL=true`'da bu iki koşul aranmaz (haritada kilit yok). Sonraki grup başlanmamışsa ve sınır doluysa işaretçi yerinde kalır, `blockedReason: "open_group_limit"` (kipten bağımsız); sonraki grubun önceki seviyesi bitmemişse sınır uygulanmaz. Önceki seviye bitince sayı sınırı aşabilir (ör. 5/3); yeni grup sayı 3'ün altına inene kadar açılmaz. Engel kalkınca aynı istek ilerletir. Birden çok neden varsa öncelik: `previous_not_seen`, `previous_level`, `open_group_limit`. Gövdesiz istek (yayındaki istemci) yalnızca işaretçideki grup **tamamlanmışsa** ilerler, seviye geçişinde seviyenin tamamlanması aranmaz; sınır ona da uygulanır, nedeni yalnızca `""` ya da `"open_group_limit"` olur |

### Kapı kipleri (`GATE_ENFORCEMENT`)

Açık grup sınırı, haritanın sıralı kilidi (`map_locked`) gibi kapı kipine bağlıdır: `off`'ta denetlenmez, `log`'da kabul edilip raporlanır (`gate_violation ... reason=open_group_limit`), `enforce`'ta `403 open_group_limit` ile reddedilir. İşaretçi uçları (`current-group`, `advance-group`) kipten bağımsızdır. `TEST_MODE_UNLOCK_ALL=true` sınırı da kapatır. Neden `open_group_limit` sprint 1 notlarındaki "Nedenler" listesine eklendi.

Dürüst istemci haritada kilitli gruba girmediği için ihlal üretmez. Yayındaki istemci "Bilmiyorum"u yazmadığı için onun kullanıcısında "görüldü" pratikte "tamamlandı" ile aynıdır ve en fazla bir açık grup oluşur; sınır ona takılmaz. İstisna, Firestore'dan taşınan kutu 1 kayıtlarıdır: böyle bir kaydı olan grup görülmüş ama tamamlanmamış sayılır, sonraki grubu açar ve açık kalır. Bu kayıtlar aynı seviyede ya da önceki seviyesi tamamlanmış seviyelerde birkaç grupta varsa eski istemcinin kullanıcısı da sınıra ulaşabilir (önceki seviyesi tamamlanmamış seviyedeki taşınmış kayıtlar sayılmaz); eski istemci kilit nedenini (`lockReason`) okumaz, grubu yalnızca "kilitli" gösterir. Yayındaki istemci `open_group_limit` kodunu tanımaz, genel hata gösterir (kabul edilen durum).

### Veritabanı

Şema değişikliği yok. Açık grup sayımı kullanıcının kaydı olan grupları tarar, sonra açık grupların bulunduğu her seviye için önceki seviyenin tamamlanmasını bir kez sorar (rozet varsa tek sorgu, yoksa önceki seviyenin sayımı); harita isteğinde ve başlanmamış grubun ilk kelimesinde bu sorgular eklenir.

### Yayın sırası

1. Backend'i deploy et. Doğrulama: `GET /api/v1/map/open-groups` (token ile) `200` ve `maxOpenGroups: 3` dönmeli; harita yanıtında `lockReason` alanı görünmeli.
2. "Bilmiyorum"u `POST /progress/unknown` ile yazan mobil sürümü backend canlıdayken yayınla, **önce değil**: eski backend'de uç `404` döner ve "Bilmiyorum" kaydedilmez (grup görülmüş sayılmaz, sonraki grup eskisi gibi tamamlanınca açılır).
3. `log` kipinde birkaç gün `reason=open_group_limit` satırlarına bak: yeni istemciden gelmemeli.

### Geri alma

Bir önceki commit'i deploy etmek şemayı bozmaz. Eski sunucu kutu 1 kayıtlarını "kalan" sayar: "Bilmiyorum" ile kaydedilmiş kelimeler kalan sayılmaya devam eder, ama sonraki grup yeniden yalnızca önceki grup tamamlanınca açılır. Sınırla yeni açılmış (başlanmış) gruplar başlanmış olduğu için açık kalır. Yeni istemcinin `/progress/unknown` ve `/map/open-groups` çağrıları `404` alır.

## Zorunlu güncelleme (en düşük uygulama sürümü)

Sunucudan tek ayarla eski mobil sürümleri güncellemeye zorlamak içindir. Karar sunucuda verilir, istemci yalnızca sonucu uygular. Ayarların hepsi **isteğe bağlıdır**; hiçbiri girilmezse hiçbir istemci engellenmez. Açılış logunda geçerli değerler `sürüm denetimi ios: en düşük=... en güncel=... mağaza=...` satırlarında görünür.

| Anahtar | Örnek | Anlamı |
|---|---|---|
| `MIN_APP_VERSION_IOS` | `1.0.0` | Altındaki iOS sürümleri zorunlu güncellemeye düşer |
| `MIN_APP_VERSION_ANDROID` | `1.0.0+3` | Android için aynı |
| `LATEST_APP_VERSION_IOS` | `1.1.0` | Altındakilere kapatılabilir "Yeni sürüm hazır" diyaloğu gösterilir (aynı sürüm için bir kez) |
| `LATEST_APP_VERSION_ANDROID` | `1.1.0` | Android için aynı |
| `APP_STORE_URL_IOS` | `https://apps.apple.com/app/id<numara>` | "Güncelle" butonunun açacağı adres. Boşsa uygulama App Store'un ana sayfasını açar |
| `APP_STORE_URL_ANDROID` | (varsayılan) `https://play.google.com/store/apps/details?id=com.kaipra.kutularlaIngilizce` | Android adresi |

**Sürüm karşılaştırma:** pubspec biçimi (`1.2.0+15`). Ad kısmı sayısal karşılaştırılır, eksik parça 0'dır (`1.2` = `1.2.0`). Sınır `+build` içeriyorsa adlar eşitken build de karşılaştırılır (`1.2.0+14` < `1.2.0+15`); sınırda build yoksa yalnızca ad. Ayrıştırılamayan istemci sürümü, bilinmeyen platform ya da build'siz istemci sürümü build'li sınırla eşit adda ise istemci **engellenmez** (fail-open). Boş sınır = sınır yok.

**Uçlar:**

- `GET /api/v1/app/version-check?platform=ios|android&version=1.2.0+15` — kimliksiz, genel hız sınırına tabi, `Cache-Control: no-store`. Sorgu yoksa `X-App-Platform` / `X-App-Version` başlıkları okunur. Her zaman `200 {"updateRequired":bool,"updateAvailable":bool,"minVersion":"","latestVersion":"","storeUrl":""}`; eksik ya da bilinmeyen değerde iki bayrak `false` (400 dönmez). `updateRequired` iken `updateAvailable` da `true`'dur.
- Diğer bütün `/api/v1` uçları (`/health` ve `/app/version-check` hariç): istek `X-App-Platform` **ve** `X-App-Version` taşıyorsa ve sürüm o platformun `MIN_APP_VERSION_*`'ının altındaysa `426 {"code":"upgrade_required","error":"Bu sürüm artık desteklenmiyor. Lütfen uygulamayı güncelleyin.","storeUrl":"..."}`. Denetim kimlik doğrulamadan önce çalışır (giriş uçları da dahil). Başlıklardan biri yoksa (yayındaki eski istemci) engellenmez.

**Bir güncellemeyi zorlamak:**

1. Yeni sürüm mağazada **yayında** olsun (incelemeden geçmiş ve kullanıcıya görünür). Önce değil: kullanıcı "Güncelle"ye basıp mağazada eski sürümü bulur ve uygulamaya giremez.
2. Coolify → Environment'ta `MIN_APP_VERSION_IOS` / `MIN_APP_VERSION_ANDROID`'i yeni sürüme (gerekirse `+build` ile) ayarla.
3. Redeploy et (ortam değişkenleri yalnızca açılışta okunur). Açılış logunda `sürüm denetimi` satırlarını ve `config: uyarı:` olmadığını kontrol et.
4. Doğrula: `curl -s 'https://<domain>/api/v1/app/version-check?platform=ios&version=0.0.1'` → `"updateRequired":true`.

Yumuşak duyuru için yalnızca `LATEST_APP_VERSION_*` ayarlanır. Geri almak için değişkeni boşalt (ya da eski değere çek) ve redeploy et.

> `APP_STORE_URL_IOS`'u App Store kimliği (App Store Connect → Uygulama → Genel Bilgi → Apple ID) belli olunca bir kez gir: `https://apps.apple.com/app/id<Apple ID>`. Boşken `MIN_APP_VERSION_IOS` ayarlanırsa production açılış loguna uyarı düşer.

## Parola sıfırlama desteği (elle)

Uygulamada parola sıfırlama henüz yok. "Parolamı unuttum" penceresi kullanıcıyı
`devruler.support@gmail.com` adresine **kayıtlı e-posta adresinden** yazmaya
yönlendirir. Böyle bir e-posta gelince:

1. E-postanın gönderen adresi, sıfırlanacak hesabın adresiyle aynı mı bak. Değilse
   sıfırlama yapma (başkasının hesabı olabilir); kayıtlı adresinden yazmasını iste.
2. Backend klasöründe: `make password-reset-sql EMAIL=<adres>`. Komut bir geçici parola
   üretir ve canlı veritabanında çalıştırılacak bloğu yazdırır; veritabanına kendisi
   bağlanmaz, parolayı hiçbir yere kaydetmez.
3. Coolify → kelimeler-db → Terminal'e bloğun tamamını yapıştır. Çıktı
   `UPDATE 1`, `DELETE n`, `UPDATE 1`, `COMMIT` olmalı. `UPDATE 0` hesap yok ya da
   hesabın parola girişi yok (Google/Apple ile açılmış) demektir; hiçbir şey
   değişmez, oturumlar da kapanmaz.
4. Geçici parolayı yalnızca kullanıcının e-postasına yanıt olarak ilet. Sıfırlama
   kullanıcının bütün cihazlardaki oturumlarını kapatır.

## Çevrimdışı mod, aşama 0 (backend): sürüm notları

Karar ve sözleşme: mobil depodaki `docs/CEVRIMDISI_MOD.md` ("API sözleşmesi"). Bu aşama yalnızca sunucu hazırlığıdır; yayındaki mobil istemci hiçbir yeni alan göndermez ve davranışı **değişmez**: mevcut uçların yanıtları aynıdır, yeni istek alanları isteğe bağlıdır. Backend, çevrimdışı modlu mobil sürümden **önce** yayına çıkar.

### Yeni ve değişen uçlar

| Uç | Ne değişti |
|---|---|
| `POST /progress/learn`, `/progress/unknown`, `/progress/swipe` | Gövdeye isteğe bağlı `opId` (UUID) ve `answeredAt` (RFC3339). İkisi de yoksa davranış aynı. `opId` geçersizse `400 invalid_request`; aynı `opId` daha önce uygulanmışsa hiçbir şey yapılmaz ve **ilk yanıt** döner (aynı `opId` başka türde bir işlem için kullanılmışsa `409 op_id_conflict`). `answeredAt` `[şimdi − 7 gün, şimdi + 5 dk]` aralığına kırpılır (ileri kayma ertesi güne taşıyorsa şimdi sayılır); Leitner tarihleri ve vade cevabın anına göre hesaplanır. `answeredAt` geçmiş bir güne (Europe/Istanbul) düşüyorsa cevap bugünkü sayaçlara (`reviewAnswersToday`) ve tekrar kapısına girmez; öğrenme ve kutu 1'deki doğru cevap **o günün** grup açılışlarına (`daily_group_unlocks`) göre denetlenir |
| `POST /progress/sync` | Yeni. Gövde `{"ops":[...]}`, en çok 200 işlem (fazlası `400 too_many_ops`). Her işlemde `opId` (UUID) zorunlu, `kind`: `learn`, `unknown` (`wordId`), `swipe` (`wordId`, `direction`), `group_started` (`level`, `group`, `usageDate`), `advance_group` (`level`, `group`), `daily_login` (`date`); hepsinde isteğe bağlı `answeredAt` (yoksa şimdi). İşlemler sırayla, her biri kendi transaction'ında uygulanır; yanıt `200 {"results":[{"opId","status","code"?,"progress"?}],"user":{...},"usage":{...}}`. Ayrıntılar aşağıda |
| `GET /sync/snapshot?dictionaryVersion=N&haveGroups=A1:1,A1:2` | Yeni. Çevrimdışı modun anlık görüntüsü; bölümler aşağıda |
| `POST /auth/refresh` | Yanıtı kaybolan yenileme 60 sn içinde aynı (artık iptal edilmiş) token'la yeniden denenirse **aynı refresh token** döner (eskiden her denemede yeni bir token). Access token yeniden imzalanır. Yeniden kullanım tespiti değişmedi |
| `POST /users/me/usage/group-started`, `/group-unlocked-by-ad` (gövdeli) | Yanıt aynı; açılış ayrıca `daily_group_unlocks`'a (`via` = `free` / `ad`) yazılır |

#### `POST /progress/sync` sonuçları

- `status`: `applied` (uygulandı), `noop` (geçerli ama hiçbir şey değişmedi: vadesi gelmemiş kelime, zaten ilerlemiş kelimeye learn, kaydı olan kelimeye unknown, bugün zaten açık grup, zaten kayıtlı giriş, ilerlemeyen işaretçi), `rejected` (uygulanmadı).
- `code` yalnızca bilgi varsa gelir. Ret kodları: `invalid_op` (bozuk işlem, eksik/geçersiz `opId`, bilinmeyen `kind`, eksik `wordId`, geçersiz `direction`, çözülemeyen `answeredAt`), `op_id_conflict`, `unknown_word`, `not_found` (swipe'ta kelimenin kaydı yok), `invalid_group`, `invalid_date`, `gate_required`, `review_gate_required`, `open_group_limit`. Uygulanan işlemde: `free_group_conflict` (`group_started`, geçmiş gün); `advance_group` noop'unda `blockedReason` (`previous_not_seen`, `previous_level`, `open_group_limit`; ilerlemeyen başka durumda kod yok).
- `progress`: kelime işlemlerinde kelimenin güncel kaydı (`GET /progress/word/:id` biçimi); reddedilen kelime işleminde de kayıt varsa gelir. Diğer işlemlerde yok.
- Aynı `opId` ikinci kez gelirse (aynı parçada, sonraki parçada ya da önce tek tek uçtan gönderilmişse) ilk sonuç aynen döner. Reddedilen işlem kaydedilmez: kapı geçildikten sonra aynı `opId` ile yeniden gönderilirse uygulanabilir.
- Veritabanı hatasında `500`: o ana kadar uygulananlar kayıtlıdır, aynı parçayı yeniden göndermek onları ikinci kez uygulamaz.
- **Bugünün** tekrar cevabında kapı bir aralık (30 cevap) aşılabilir: 30 kapısı geçilmemişken 31.–60. cevap kabul, 61. `enforce`'ta `review_gate_required` (`log`'da kabul + rapor). Geçmiş günün tekrar cevabı kapıya sorulmaz.
- `group_started`: `usageDate` bugünse `group-started` ile aynı (sayaç, küme, kapı). Geçmiş günse (en çok 7 gün önce; daha eski `invalid_date`, gelecek gün bugün sayılır) yalnızca `daily_group_unlocks`'a `offline_free` yazılır; o günün ücretsiz hakkı başka bir gruba kullanılmışsa yine yazılır, kod `free_group_conflict`.
- `advance_group`: gövdeli `advance-group` ile aynı.
- `daily_login`: `date` bugün ya da en çok 7 gün önce (`YYYY-MM-DD`); `created_at` o güne yazılır (seri hesabı senkronizasyon gününü girişli saymaz).
- `user` = `GET /auth/me` yanıtındaki `user` nesnesi; `usage` = `GET /users/me/usage` yanıtı.

#### `GET /sync/snapshot` bölümleri

Her bölüm, adı geçen ucun yanıtını kuran kodla üretilir; tek alanlı sarmalayıcılar açılmıştır:

| Alan | Karşılığı |
|---|---|
| `serverNow` | Sunucu saati (RFC3339, UTC) |
| `usageDate` | Bugün (`YYYY-MM-DD`, Europe/Istanbul) |
| `dictionaryVersion` | Sözlük sürümü: uygulanmış sözlük yaması sayısı (`dictionary_patches_applied`) |
| `user` | `GET /auth/me` → `user` |
| `usage` | `GET /users/me/usage` (gün dönümü uygulanmış) |
| `progress` | Kutu 1–5'teki bütün kayıtlar, `GET /progress/word/:id` biçiminde, `wordId` sırasıyla |
| `learnedCount` | `GET /home/learned-count` → `learnedCount` (kutu 6) |
| `levels` | `{"A1": GET /map/levels/A1/groups, ..., "C2": ...}`, altı seviye |
| `openGroups` | `GET /map/open-groups` |
| `cards` | Gereken grupların kartları: ilerlemesi olan her grup (kelimenin sözlükteki grubu), işaretçideki grup ve sonraki grup; `GET /dictionary/words` biçiminde, seviye/grup/sıra sırasıyla. İstemci elindeki grupları `haveGroups` (virgülle ayrılmış `SEVIYE:GRUP`) ile bildirir: `dictionaryVersion` sunucununkiyle aynıysa yalnızca bu gruplar atlanır, gereken diğer grupların kartları her zaman gelir. Sürüm verilmemiş ya da farklıysa `haveGroups` yok sayılır, gereken bütün gruplar gelir. Tanınmayan parçalar yok sayılır |
| `stats` | `GET /stats/summary`; tek fark: anlık görüntü yeni rozetleri yazmaz, `newlyEarnedBadges` her zaman `[]` (bildirim bir sonraki `/stats/summary`'de çıkar) |
| `loginDates` | `GET /stats/login-dates` → `dates` |

**Sözlük sürümü:** yayındaki sözlük yalnızca yamalarla (`seed/dictionary_patches`) değiştiği için her yeni yama sürümü bir artırır ve istemci kartları yeniden indirir. Sözlüğü yama dışında (elle SQL, `import-dictionary`) değiştirmek sürümü **artırmaz**; istemcilerin önbelleği eski kalır. Kart metnini her zaman yamayla düzelt.

### Açılışta AutoMigrate'in uygulayacağı veritabanı değişikliği

| Tablo | Değişiklik | Not |
|---|---|---|
| `applied_ops` | Yeni: `user_id uuid`, `op_id uuid` (PK ikisi), `kind`, `applied_at`, `result jsonb`, `review_day date NULL`; `applied_at` index'i | Kimlikli işlemin ilk sonucu; işlemle aynı transaction'da yazılır. Kullanıcı silinince CASCADE. 30 günden eski satırlar saatlik temizlikte silinir (log: `uygulanmış işlem temizliği: N satır silindi`) |
| `daily_group_unlocks` | Yeni: `user_id`, `usage_date date`, `level`, `grp`, `via` (`free`/`ad`/`nofill`/`offline_free`, CHECK), `created_at`; UNIQUE (`user_id`, `usage_date`, `level`, `grp`) | Grup açılışlarının günlük geçmişi. Kullanıcı silinince CASCADE; temizlenmez (satır başına birkaç yüz bayt, kullanıcı başına günde birkaç satır). `nofill` şimdilik yazılmaz: yayındaki istemci no-fill geçişinden sonra da `group-unlocked-by-ad` çağırır |

Var olan tablolara dokunulmaz. Yayından önce açılmış bugünkü grupların satırı yoktur; aynı gruba yeniden girişte (`group-started`/`by-ad` yeniden çağrılınca) tamamlanır.

### Davranış ve izleme

Yeni rapor nedenleri (`gate_violation ...` satırları ve `log` kipinde Sentry uyarısı; `off`'ta raporlanmaz):

- `kind=learn reason=past_day_not_unlocked`: geçmiş günün öğrenme cevabı, grubun o gün açıldığına dair kayıt olmadan geldi. `enforce`'ta `gate_required` ile reddedilir.
- `kind=review reason=past_day_review_overshoot answers_today=N`: geçmiş bir günün tekrar cevapları 120'yi aştı (sayım kimlikli cevaplardan, `applied_ops.review_day`). Yalnızca raporlanır, reddedilmez.
- `kind=learn reason=offline_free_group_conflict`: çevrimdışı geçmiş günde ücretsiz açılan grup, o günün ücretsiz hakkı başka gruba kullanılmışken geldi. Yalnızca raporlanır.

Kabul edilen taviz (sözleşme): saatini geri alan bir istemci geçmiş gün kuralıyla tekrar kapısını atlatabilir (en çok 7 gün geriye); bu durum `past_day_review_overshoot` ile görünür.

**Yeniden bağlanma yoklaması:** mobil istemci bağlantının geri geldiğini `GET /api/v1/health` ile anlar. Uç kimliksizdir, zorunlu güncelleme denetiminden muaftır ve tek bir `COUNT(*)` (sözlük) sorgusu yapar (2 sn zaman aşımı, veritabanı yoksa `503`). Genel hız sınırına (`API_RATE_LIMIT`, varsayılan 600/dk, token yoksa IP başına) tabidir: yoklama geri çekilmeli (backoff) olmalıdır.

**Refresh token:** rotasyonda yeni refresh token eskisinden türetilir (JWT gizli anahtarıyla HMAC). `JWT_SECRET` değişirse tolerans içindeki yeniden deneme aynı token'ı üretemez ve eski davranışa (yeni çift) düşer; oturumlar düşmez.

### Yayın sırası

1. Yedek al (PostgreSQL → Backups → Backup Now). Backend'i `GATE_ENFORCEMENT` mevcut değeriyle deploy et. Açılış logunda `db: migrate tamamlandı`.
2. Doğrula (token ile): `GET /api/v1/sync/snapshot` → `200`, gövdede `dictionaryVersion` ve `levels`; `POST /api/v1/progress/sync` gövdesi `{"ops":[]}` → `200 {"results":[],...}`.
3. Çevrimdışı modlu mobil sürümü bundan **sonra** yayınla. Eski sunucu bilinmeyen alanları yok sayar, `/progress/sync` ve `/sync/snapshot` `404` döner (istemci işlemleri tek tek uçlardan gönderir).

### Geri alma

Bir önceki commit'i deploy etmek şemayı bozmaz (yeni tablolar kalır, eski kod yok sayar). Yeni mobil sürüm yayındaysa: `/progress/sync` ve `/sync/snapshot` `404` döner; tek tek uçlar `opId`/`answeredAt`'i yok sayar, yani yeniden gönderilen bir cevap ikinci kez uygulanabilir (eski idempotentlik kuralları geçerli: vadesi gelmemiş kelime ilerlemez) ve geçmiş günün cevabı bugünün cevabı sayılır. Türetilmiş refresh token'lar geçerli kalır; eski sunucu tolerans içindeki yeniden denemede yine yeni bir çift verir.
