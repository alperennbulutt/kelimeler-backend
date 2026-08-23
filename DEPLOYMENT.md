# Production Deploy — Coolify (GitHub + otomatik domain)

Bu rehber backend'i Coolify ile sunucuya (45.87.120.58) deploy eder.
Mimari: **Coolify Application (Go container)** + **Coolify managed PostgreSQL** + **Traefik (otomatik SSL'li domain)**.

> Port derdi yok: Coolify her uygulamayı Traefik reverse-proxy arkasında domain bazlı yönlendirir; container içinde 8080 dinlemen yeterli.

---

## 1) Backend'i GitHub'a push et

`backend/` klasörü bir GitHub reposu olmalı. `.gitignore` sayesinde `secrets/`, `.env` push **edilmez** (sadece `.env.production.example` ve `seed/` gider).

**Seçenek A — Ayrı repo (önerilen, en temiz):**
```bash
cd backend
git init -b main
git add .
git commit -m "kelimeler backend"
git remote add origin git@github.com:KULLANICI_ADI/kelimeler-backend.git
git push -u origin main
```

**Seçenek B — Monorepo:** mevcut Flutter repo'sunu push et; Coolify'de aşağıda "Base Directory" = `backend` verirsin.

---

## 2) Coolify'de PostgreSQL oluştur

- Coolify panel → Projeni seç → **+ New Resource → Database → PostgreSQL** (16)
- Oluşunca **"Postgres URL (internal)"** değerini kopyala — Application'ın `DATABASE_URL`'i bu olacak.
  Örnek: `postgres://postgres:xxxx@abc123:5432/postgres`

---

## 3) Coolify'de Application oluştur

- **+ New Resource → Application → Private/Public Repository** → GitHub reposunu seç
- **Build Pack:** `Dockerfile`
- (Monorepo ise) **Base Directory:** `backend`
- **Port (Ports Exposes):** `8080`
- **Health Check Path:** `/api/v1/health`

---

## 4) Environment Variables (Application → Environment)

`.env.production.example` referansıyla şunları gir:

| Anahtar | Değer |
|---|---|
| `APP_ENV` | `production` |
| `PORT` | `8080` |
| `DATABASE_URL` | Adım 2'deki internal URL (DB adı URL'in sonunda; ör. `.../postgres`) |
| `JWT_SECRET` | `openssl rand -base64 48` çıktısı (güçlü, gizli) |
| `ACCESS_TOKEN_TTL` | `15m` |
| `REFRESH_TOKEN_TTL` | `720h` |
| `GOOGLE_CLIENT_IDS` | `928054180858-...apps.googleusercontent.com` |
| `APPLE_CLIENT_ID` | iOS bundle id'n (Apple yoksa boş) |

---

## 5) Domain + Deploy

- Application → **Generate Domain** (Coolify otomatik `https://...sslip.io` domaini + Let's Encrypt SSL verir)
- **Deploy** → Coolify Dockerfile'ı build edip başlatır.
- Test: tarayıcıda `https://<domain>/api/v1/health` → `{"status":"ok"}`

---

## 6) Kelime sözlüğünü yükle (yalnızca BİR kez)

Deploy sonrası DB boştur. 9980 kelimeyi yüklemek için Application'ın **Terminal**'inden (Coolify → Application → Terminal):

```sh
psql "$DATABASE_URL" -f /app/seed/dictionary_words.sql
```

Doğrula:
```sh
psql "$DATABASE_URL" -c "SELECT count(*) FROM dictionary_words;"   # 9980 dönmeli
```

> Not: Tekrar yüklemek istersen önce `TRUNCATE dictionary_words;` çalıştır. İleride Firebase'de kelime değişirse, lokal `make import-dictionary` + yeni seed dump alıp tekrar yükleyebilirsin.

---

## 7) Flutter'ı production'a yönlendir

Uygulama build alırken backend adresini ver:

```bash
flutter build apk --release --dart-define=API_BASE_URL=https://<coolify-domain>/api/v1
# iOS:
flutter build ipa --release --dart-define=API_BASE_URL=https://<coolify-domain>/api/v1
```

---

## Gizlilik politikası sayfası

Mağaza listelemesi için gereken herkese açık politika sayfası, ayrı bir statik
site kaynağı yerine bu uygulamanın içinden servis edilir:

- `https://<domain>/privacy` (ayrıca `/privacy-policy`, `/gizlilik`; kök adres `/privacy`'ye yönlenir)
- Kaynak: `internal/web/privacy-policy.html`, `go:embed` ile binary'ye gömülür.

Metni güncellerken **üç kopyayı birlikte güncelle**, aksi halde birbirinden ayrışırlar:

1. `backend/internal/web/privacy-policy.html` (yayınlanan sayfa)
2. Flutter reposundaki `docs/privacy-policy.html`
3. Uygulama içi sayfa: `lib/features/legal/presentation/privacy_policy_page.dart`

Sayfanın başındaki "Son Güncelleme" tarihini de değiştirmeyi unutma.

---

## Notlar

- **Güncelleme:** `git push` → Coolify otomatik yeniden deploy eder (webhook).
- **Google/Apple Sign-In:** OAuth client'lar Google Cloud / Apple Developer tarafında. Backend yalnızca idToken **doğrular**; production domaini eklemek gerekmez ama Google Cloud Console'da OAuth ayarlarının geçerli olduğundan emin ol.
- **Güvenlik:** `JWT_SECRET`'i kimseyle paylaşma; sunucu root şifreni deploy sonrası değiştir.
- **Crash reporting:** Şu an kapalı. İstenirse `sentry_flutter` + backend log toplama eklenebilir.
