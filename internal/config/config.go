package config

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

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
func (c *Config) Warnings() []string {
	if !c.IsProduction() {
		return nil
	}
	var w []string
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
	return w
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
