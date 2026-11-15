package config

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"kelimeler-backend/internal/appversion"

	"github.com/joho/godotenv"
)

// Geçerli APP_ENV değerleri. Başka bir değer (boş, "prod", "Production" ...)
// kabul edilmez: production'a özgü korumalar (sayaç sıfırlama ucunun kapanması,
// CORS, JWT_SECRET denetimi) yalnızca tam "production" ile devreye girer ve
// yazım hatası sunucuyu sessizce geliştirme kipinde açardı.
const (
	EnvProduction  = "production"
	EnvDevelopment = "development"
)

// GateEnforcement, reklam kapısı ihlallerine (açılmamış gruba öğrenme, geçiş
// kaydı olmadan kontrol noktası sonrası tekrar cevabı) sunucunun tepkisidir.
type GateEnforcement string

const (
	// GateOff: ihlal denetlenmez.
	GateOff GateEnforcement = "off"
	// GateLog: ihlal loglanır ve Sentry'ye bilgi olarak gider, istek kabul edilir.
	GateLog GateEnforcement = "log"
	// GateEnforce: ihlal eden istek 403 ile reddedilir.
	GateEnforce GateEnforcement = "enforce"
)

// DefaultGateEnforcement, GATE_ENFORCEMENT verilmezse kullanılır: yayındaki
// istemciyi bozmadan ihlalleri görünür kılar.
const DefaultGateEnforcement = GateLog

// DefaultTrustedProxies, TRUSTED_PROXIES verilmezse güvenilen proxy adresleridir:
// özel ağlar ve loopback. Coolify'nin Traefik'i container'a docker ağından
// (özel bir adresten) bağlanır; internetten doğrudan gelen bir bağlantı bu
// aralıklarda olamaz, dolayısıyla başlığı uyduramaz.
var DefaultTrustedProxies = []string{
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8", "::1/128", "fc00::/7",
}

// DefaultGuestRefreshTokenTTL, GUEST_REFRESH_TOKEN_TTL verilmezse kullanılır (1 yıl).
const DefaultGuestRefreshTokenTTL = 8760 * time.Hour

// DefaultAppStoreURLAndroid, APP_STORE_URL_ANDROID verilmezse "Güncelle"
// butonunun açtığı Play Store adresidir.
const DefaultAppStoreURLAndroid = "https://play.google.com/store/apps/details?id=com.kaipra.kutularlaIngilizce"

// MinJWTSecretBytes, production'da kabul edilen en kısa JWT_SECRET uzunluğudur
// (HS256 anahtarı en az özet boyu kadar olmalı).
const MinJWTSecretBytes = 32

// placeholderJWTSecrets, depoda açıkça duran JWT_SECRET değerleridir (kod
// varsayılanı ve .env*.example yer tutucuları). Herkes bildiği için bunlarla
// imzalanan token'lara güvenilemez; production'da reddedilirler.
var placeholderJWTSecrets = []string{
	"dev-secret-change-me",
	"change-me-to-a-long-random-string",
	"BURAYA_UZUN_RASTGELE_DEGER_KOYUN",
}

// Config, uygulamanın tüm ortam yapılandırmasını tutar.
type Config struct {
	Port        string
	AppEnv      string
	DatabaseURL string

	// Veritabanı bağlantı havuzu ve sorgu zaman aşımı (bkz. db.Options).
	DBMaxOpenConns     int
	DBMaxIdleConns     int
	DBConnMaxLifetime  time.Duration
	DBStatementTimeout time.Duration

	JWTSecret       string
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration

	// GuestRefreshTokenTTL, misafir hesaplarına verilen refresh token'ların
	// ömrüdür (rotasyonda da). Misafirin başka giriş yolu yoktur: token
	// kaybolursa ya da süresi dolarsa hesaba bir daha ulaşılamaz. 30 günlük
	// olağan ömür, bir ay uygulamayı açmayan misafiri ilerlemesinden koparırdı.
	GuestRefreshTokenTTL time.Duration

	// Google Sign-In: kabul edilen audience (client ID) listesi.
	GoogleClientIDs []string

	// Apple Sign-In.
	AppleClientID string
	AppleIssuer   string

	// Apple hesap silme (token revoke) için gereken kimlik bilgileri.
	// Üçü birden verilmezse revoke devre dışı kalır; hesap silme yine çalışır.
	AppleTeamID     string
	AppleKeyID      string
	ApplePrivateKey string

	// Sözlük seed'inin (dictionary_words.sql) ve yamalarının
	// (dictionary_patches/) bulunduğu klasör. Varsayılan imajdaki /seed'dir;
	// lokalde depodaki ./seed verilebilir (bkz. DEPLOYMENT.md, 6. bölüm).
	SeedDir string

	// Sadece import-dictionary aracı kullanır.
	FirebaseProjectID      string
	FirebaseServiceAccount string

	// AppConstants.isTestMode karşılığı: tüm grupları kilitsiz açar.
	TestModeUnlockAll bool

	// Günlük kullanım sayaçlarının gün sınırını belirleyen IANA saat dilimi adı
	// (sayaçlar bu dilimin gece yarısında sıfırlanır).
	UsageTimezone string

	// Reklam kapısı ihlallerinin ele alınışı (GATE_ENFORCEMENT: off | log | enforce).
	GateEnforcement GateEnforcement

	// --- Gözlemlenebilirlik ---

	// Boşsa hata raporlama devre dışı kalır.
	SentryDSN     string
	SentryRelease string

	// SentryRelayDSN, mobil uygulamanın gerçek Sentry DSN'idir
	// (SENTRY_RELAY_DSN). Verilirse sunucu POST /api/v1/sentry/api/:projectId/envelope
	// ucunda uygulamanın hata raporlarını Sentry'ye aktarır (bkz. DEPLOYMENT.md,
	// "Sentry rölesi"). SentryRelay, ayrıştırılmış hâlidir; boş ya da geçersizse
	// nil'dir ve röle kapalıdır.
	SentryRelayDSN string
	SentryRelay    *SentryDSN

	// --- Güvenlik ---

	// CORS'a izin verilen origin listesi. Boşsa production'da hiçbir origin'e
	// izin verilmez: yanıtlara CORS başlığı yazılmaz, tarayıcı başka origin'den
	// gelen isteğin yanıtını okutmaz (mobil istemci CORS'tan etkilenmez).
	CORSAllowedOrigins []string

	// İstemci IP'sinin okunduğu başlık (varsayılan X-Forwarded-For). Başlığa
	// yalnızca isteği TrustedProxies'teki bir adres ilettiyse güvenilir; aksi
	// halde bağlantının karşı ucu istemci sayılır, böylece doğrudan bağlanan
	// biri başlığı uydurarak hız sınırını atlatamaz.
	ProxyHeader    string
	TrustedProxies []string

	// Kimlik üreten auth uçlarına dakikada izin verilen istek sayısı (IP başına).
	AuthRateLimit       int
	AuthRateLimitWindow time.Duration

	// Genel uçlara dakikada izin verilen istek sayısı. Geçerli access token
	// taşıyan istekler kullanıcı başına, diğerleri IP başına sayılır.
	//
	// Mobil operatörler çok sayıda kullanıcıyı tek IP arkasına koyar (CGNAT) ve
	// bir tekrar seansı dakikada onlarca istek üretir; IP başına dar bir sınır
	// kötüye kullanımı değil gerçek kullanıcıları düşürürdü.
	APIRateLimit       int
	APIRateLimitWindow time.Duration

	// HTTP sunucu zaman aşımları ve gövde sınırı.
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	IdleTimeout  time.Duration
	BodyLimit    int

	// SIGTERM/SIGINT gelince süren isteklerin bitmesi için beklenecek en uzun
	// süre. Docker'ın durdurma süresinden (varsayılan 10 sn) kısa olmalı; yoksa
	// süreç bu süre dolmadan SIGKILL ile öldürülür ve Sentry kuyruğu boşaltılamaz.
	ShutdownTimeout time.Duration

	// --- Zorunlu güncelleme ---

	// AppVersions, platform başına en düşük (MIN_APP_VERSION_*) ve en güncel
	// (LATEST_APP_VERSION_*) istemci sürümü ile mağaza adresidir
	// (APP_STORE_URL_*). Hepsi isteğe bağlıdır; sıfır değeri hiçbir istemciyi
	// engellemez.
	AppVersions appversion.Policy

	// appVersionWarnings, okunurken yok sayılan geçersiz sürüm/adres
	// ayarlarıdır; ortamdan bağımsız olarak Warnings'te döner.
	appVersionWarnings []string

	// sentryRelayWarning, SENTRY_RELAY_DSN ayrıştırılamadıysa uyarıdır;
	// ortamdan bağımsız olarak Warnings'te döner.
	sentryRelayWarning string
}

// Load, .env dosyasını (varsa) yükler, Config'i okur ve doğrular. Yapılandırma
// geçersizse süreci açık bir mesajla durdurur: yanlış yapılandırılmış bir
// sunucunun açılmaması, sessizce güvensiz açılmasından iyidir.
func Load() *Config {
	// .env yoksa sessizce devam et (production'da gerçek env kullanılır).
	if err := godotenv.Load(); err != nil {
		log.Printf("config: .env bulunamadı, ortam değişkenleri kullanılacak (%v)", err)
	}

	cfg, err := FromEnv()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	return cfg
}

// FromEnv, Config'i ortam değişkenlerinden okur ve Validate ile doğrular.
func FromEnv() (*Config, error) {
	cfg := &Config{
		Port:                   getEnv("PORT", "8080"),
		AppEnv:                 os.Getenv("APP_ENV"),
		DatabaseURL:            getEnv("DATABASE_URL", "postgres://postgres:postgres@localhost:5434/kelimeler?sslmode=disable"),
		DBMaxOpenConns:         getPositiveInt("DB_MAX_OPEN_CONNS", 25),
		DBMaxIdleConns:         getPositiveInt("DB_MAX_IDLE_CONNS", 10),
		DBConnMaxLifetime:      getDuration("DB_CONN_MAX_LIFETIME", time.Hour),
		DBStatementTimeout:     getDuration("DB_STATEMENT_TIMEOUT", 10*time.Second),
		JWTSecret:              getEnv("JWT_SECRET", "dev-secret-change-me"),
		AccessTokenTTL:         getDuration("ACCESS_TOKEN_TTL", 15*time.Minute),
		RefreshTokenTTL:        getDuration("REFRESH_TOKEN_TTL", 720*time.Hour),
		GuestRefreshTokenTTL:   getDuration("GUEST_REFRESH_TOKEN_TTL", DefaultGuestRefreshTokenTTL),
		GoogleClientIDs:        getList("GOOGLE_CLIENT_IDS"),
		AppleClientID:          getEnv("APPLE_CLIENT_ID", ""),
		AppleIssuer:            getEnv("APPLE_ISSUER", "https://appleid.apple.com"),
		AppleTeamID:            getEnv("APPLE_TEAM_ID", ""),
		AppleKeyID:             getEnv("APPLE_KEY_ID", ""),
		ApplePrivateKey:        getEnv("APPLE_PRIVATE_KEY", ""),
		SeedDir:                getEnv("SEED_DIR", "/seed"),
		FirebaseProjectID:      getEnv("FIREBASE_PROJECT_ID", ""),
		FirebaseServiceAccount: getEnv("FIREBASE_SERVICE_ACCOUNT", ""),
		TestModeUnlockAll:      getBool("TEST_MODE_UNLOCK_ALL", false),
		UsageTimezone:          getEnv("USAGE_TIMEZONE", "Europe/Istanbul"),
		GateEnforcement:        GateEnforcement(getEnv("GATE_ENFORCEMENT", string(DefaultGateEnforcement))),

		SentryDSN:     getEnv("SENTRY_DSN", ""),
		SentryRelease: getEnv("SENTRY_RELEASE", ""),

		CORSAllowedOrigins: getList("CORS_ALLOWED_ORIGINS"),

		ProxyHeader:    getEnv("PROXY_HEADER", "X-Forwarded-For"),
		TrustedProxies: getListOr("TRUSTED_PROXIES", DefaultTrustedProxies),

		// Parola deneme saldırılarını yavaşlatacak kadar sıkı, gerçek
		// kullanıcıyı engellemeyecek kadar gevşek.
		AuthRateLimit:       getPositiveInt("AUTH_RATE_LIMIT", 10),
		AuthRateLimitWindow: getDuration("AUTH_RATE_LIMIT_WINDOW", time.Minute),
		APIRateLimit:        getPositiveInt("API_RATE_LIMIT", 600),
		APIRateLimitWindow:  getDuration("API_RATE_LIMIT_WINDOW", time.Minute),

		ReadTimeout:  getDuration("READ_TIMEOUT", 15*time.Second),
		WriteTimeout: getDuration("WRITE_TIMEOUT", 30*time.Second),
		IdleTimeout:  getDuration("IDLE_TIMEOUT", 60*time.Second),
		BodyLimit:    getInt("BODY_LIMIT_BYTES", 1<<20), // 1 MiB

		ShutdownTimeout: getDuration("SHUTDOWN_TIMEOUT", 8*time.Second),
	}
	cfg.AppVersions, cfg.appVersionWarnings = loadAppVersions()
	cfg.SentryRelayDSN = strings.TrimSpace(os.Getenv("SENTRY_RELAY_DSN"))
	cfg.SentryRelay, cfg.sentryRelayWarning = loadSentryRelay(cfg.SentryRelayDSN)

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// IsProduction, production ortamında çalışılıp çalışılmadığını söyler.
func (c *Config) IsProduction() bool { return c.AppEnv == EnvProduction }

// Validate, sunucunun açılmasını engellemesi gereken yapılandırma hatalarını
// döndürür. Açılışı engellemeyen ama bilinmesi gereken eksikler Warnings'tedir.
func (c *Config) Validate() error {
	switch c.AppEnv {
	case EnvProduction, EnvDevelopment:
	case "":
		return errors.New(`APP_ENV tanımlı değil: "production" ya da "development" olmalı`)
	default:
		return fmt.Errorf(`APP_ENV=%q geçersiz: tam olarak "production" ya da "development" olmalı`, c.AppEnv)
	}

	switch c.GateEnforcement {
	case GateOff, GateLog, GateEnforce:
	default:
		return fmt.Errorf(`GATE_ENFORCEMENT=%q geçersiz: "off", "log" ya da "enforce" olmalı`, c.GateEnforcement)
	}

	// time.ParseDuration negatif ve sıfır süreyi kabul eder; böyle bir ömürle
	// verilen token doğduğu anda geçersiz olurdu (misafir hesabı açılır açılmaz
	// erişilemez kalırdı).
	if c.GuestRefreshTokenTTL <= 0 {
		return fmt.Errorf("GUEST_REFRESH_TOKEN_TTL=%s geçersiz: pozitif bir süre olmalı (ör. 8760h)", c.GuestRefreshTokenTTL)
	}

	for _, p := range c.TrustedProxies {
		if _, _, err := net.ParseCIDR(p); err == nil {
			continue
		}
		if net.ParseIP(p) == nil {
			return fmt.Errorf("TRUSTED_PROXIES: %q geçerli bir IP ya da CIDR değil", p)
		}
	}

	if c.IsProduction() {
		for _, p := range placeholderJWTSecrets {
			if c.JWTSecret == p {
				return errors.New("production'da JWT_SECRET örnek/varsayılan değerde bırakılamaz; " +
					"`openssl rand -base64 48` ile üretilmiş bir değer girin")
			}
		}
		if len(c.JWTSecret) < MinJWTSecretBytes {
			return fmt.Errorf("production'da JWT_SECRET en az %d bayt olmalı (%d bayt verildi); "+
				"`openssl rand -base64 48` ile üretilmiş bir değer girin", MinJWTSecretBytes, len(c.JWTSecret))
		}
	}
	return nil
}

// Warnings, sunucunun açılmasını engellemeyen ama production'da bir özelliği
// bozan eksikleri döndürür. Açılışta loglanır ve Sentry'ye bildirilir.
//
// Geçersiz sürüm/mağaza adresi ayarları her ortamda uyarılır: yok sayılan bir
// MIN_APP_VERSION_* zorunlu güncellemenin sessizce çalışmaması demektir.
func (c *Config) Warnings() []string {
	w := append([]string(nil), c.appVersionWarnings...)
	if c.sentryRelayWarning != "" {
		w = append(w, c.sentryRelayWarning)
	}
	if !c.IsProduction() {
		return w
	}
	if c.AppleTeamID == "" || c.AppleKeyID == "" || c.ApplePrivateKey == "" {
		w = append(w, "APPLE_TEAM_ID/APPLE_KEY_ID/APPLE_PRIVATE_KEY eksik: "+
			"Apple hesap silmede token iptali YAPILMAYACAK (Apple bunu şart koşar)")
	}
	if len(c.GoogleClientIDs) == 0 {
		w = append(w, "GOOGLE_CLIENT_IDS boş: her Google girişi invalid_google_token ile reddedilecek")
	}
	if c.TestModeUnlockAll {
		w = append(w, "TEST_MODE_UNLOCK_ALL=true: tüm gruplar kilitsiz açılıyor (reklam kapıları devre dışı)")
	}
	if !c.AppVersions.IOS.Min.IsZero() && c.AppVersions.IOS.StoreURL == "" {
		w = append(w, "MIN_APP_VERSION_IOS ayarlı ama APP_STORE_URL_IOS boş: "+
			"\"Güncelle\" butonu uygulamanın sayfası yerine App Store'un ana sayfasını açacak")
	}
	return w
}

// loadAppVersions, zorunlu güncelleme ayarlarını okur. Geçersiz bir değer
// açılışı durdurmaz: uyarı döner ve değer yok sayılır (sürüm sınırı yok,
// mağaza adresi boş ya da Android'de varsayılan). Bir yazım hatası yüzünden
// sunucunun açılmaması, acil bir düzeltmenin de yayınlanamaması demekti.
func loadAppVersions() (appversion.Policy, []string) {
	var warnings []string
	version := func(key string) appversion.Version {
		raw := os.Getenv(key)
		if strings.TrimSpace(raw) == "" {
			return appversion.Version{}
		}
		v, ok := appversion.Parse(raw)
		if !ok {
			warnings = append(warnings, fmt.Sprintf("%s=%q geçersiz (beklenen ör. 1.2.0 ya da 1.2.0+15): "+
				"YOK SAYILDI, bu sınır uygulanmıyor", key, raw))
		}
		return v
	}
	storeURL := func(key, def string) string {
		raw := strings.TrimSpace(os.Getenv(key))
		if raw == "" {
			return def
		}
		if !validStoreURL(raw) {
			warnings = append(warnings, fmt.Sprintf("%s=%q geçerli bir adres değil (https://... bekleniyor): "+
				"YOK SAYILDI", key, raw))
			return def
		}
		return raw
	}
	rule := func(platform string, defURL string) appversion.Rule {
		r := appversion.Rule{
			Min:      version("MIN_APP_VERSION_" + platform),
			Latest:   version("LATEST_APP_VERSION_" + platform),
			StoreURL: storeURL("APP_STORE_URL_"+platform, defURL),
		}
		if r.Latest.Below(r.Min) {
			warnings = append(warnings, fmt.Sprintf("LATEST_APP_VERSION_%s=%q, MIN_APP_VERSION_%s=%q'dan düşük: "+
				"en güncel sürüm en düşük sürümden küçük olamaz, ayarları kontrol edin",
				platform, r.Latest.Raw, platform, r.Min.Raw))
		}
		return r
	}
	p := appversion.Policy{
		IOS:     rule("IOS", ""),
		Android: rule("ANDROID", DefaultAppStoreURLAndroid),
	}
	return p, warnings
}

// loadSentryRelay, SENTRY_RELAY_DSN'i ayrıştırır. Boşsa röle kapalıdır.
// Geçersiz değer açılışı durdurmaz: uyarı döner ve röle kapalı kalır (hata
// raporlarının gitmemesi, sunucunun açılmamasından iyidir).
func loadSentryRelay(raw string) (*SentryDSN, string) {
	if raw == "" {
		return nil, ""
	}
	d, err := ParseSentryDSN(raw)
	if err != nil {
		return nil, fmt.Sprintf("SENTRY_RELAY_DSN geçersiz (%v; beklenen https://ANAHTAR@HOST/PROJE_NO): "+
			"YOK SAYILDI, Sentry rölesi kapalı", err)
	}
	return &d, ""
}

// validStoreURL, mağaza adresinin mutlak ve uygulamanın açabileceği bir
// şemada olduğunu denetler.
func validStoreURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	switch u.Scheme {
	case "https", "http", "itms-apps", "market":
		return true
	}
	return false
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getList(key string) []string {
	raw := os.Getenv(key)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// getListOr, getList gibi okur; değişken tanımsız ya da boşsa def'in bir
// kopyasını döner.
func getListOr(key string, def []string) []string {
	if l := getList(key); len(l) > 0 {
		return l
	}
	return append([]string(nil), def...)
}

func getDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		log.Printf("config: %s geçersiz duration, varsayılan kullanılıyor", key)
	}
	return def
}

func getBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

// getInt, tam sayı ortam değişkenini okur; geçersiz/eksikse varsayılana döner.
func getInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Printf("config: %s sayıya çevrilemedi (%q), varsayılan %d kullanılıyor", key, v, def)
		return def
	}
	return n
}

// getPositiveInt, getInt gibi okur ama 0 ve negatif değerleri de reddeder.
//
// Hız sınırları için kullanılır: Fiber'ın limiter'ı Max <= 0 görünce sınırı
// kapatmaz, sessizce kendi varsayılanına (dakikada 5 istek) çeker ve API
// neredeyse kullanılamaz hale gelir.
func getPositiveInt(key string, def int) int {
	n := getInt(key, def)
	if n <= 0 {
		log.Printf("config: uyarı: %s pozitif olmalı (%d verildi), varsayılan %d kullanılıyor", key, n, def)
		return def
	}
	return n
}
