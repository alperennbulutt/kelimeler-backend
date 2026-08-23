package config

import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Config, uygulamanın tüm ortam yapılandırmasını tutar.
type Config struct {
	Port        string
	AppEnv      string
	DatabaseURL string

	JWTSecret       string
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration

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

	// --- Gözlemlenebilirlik ---

	// Boşsa hata raporlama devre dışı kalır.
	SentryDSN     string
	SentryRelease string

	// --- Güvenlik ---

	// CORS'a izin verilen origin listesi. Boşsa production'da tarayıcı
	// kaynaklı istekler reddedilir (mobil istemci CORS'tan etkilenmez).
	CORSAllowedOrigins []string

	// Auth uçlarına dakikada izin verilen istek sayısı (IP başına).
	AuthRateLimit       int
	AuthRateLimitWindow time.Duration

	// Genel uçlara dakikada izin verilen istek sayısı (IP başına).
	APIRateLimit       int
	APIRateLimitWindow time.Duration

	// HTTP sunucu zaman aşımları ve gövde sınırı.
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	IdleTimeout  time.Duration
	BodyLimit    int
}

// Load, .env dosyasını (varsa) yükler ve Config'i döndürür.
func Load() *Config {
	// .env yoksa sessizce devam et (production'da gerçek env kullanılır).
	if err := godotenv.Load(); err != nil {
		log.Printf("config: .env bulunamadı, ortam değişkenleri kullanılacak (%v)", err)
	}

	cfg := &Config{
		Port:                   getEnv("PORT", "8080"),
		AppEnv:                 getEnv("APP_ENV", "development"),
		DatabaseURL:            getEnv("DATABASE_URL", "postgres://postgres:postgres@localhost:5434/kelimeler?sslmode=disable"),
		JWTSecret:              getEnv("JWT_SECRET", "dev-secret-change-me"),
		AccessTokenTTL:         getDuration("ACCESS_TOKEN_TTL", 15*time.Minute),
		RefreshTokenTTL:        getDuration("REFRESH_TOKEN_TTL", 720*time.Hour),
		GoogleClientIDs:        getList("GOOGLE_CLIENT_IDS"),
		AppleClientID:          getEnv("APPLE_CLIENT_ID", ""),
		AppleIssuer:            getEnv("APPLE_ISSUER", "https://appleid.apple.com"),
		AppleTeamID:            getEnv("APPLE_TEAM_ID", ""),
		AppleKeyID:             getEnv("APPLE_KEY_ID", ""),
		ApplePrivateKey:        getEnv("APPLE_PRIVATE_KEY", ""),
		FirebaseProjectID:      getEnv("FIREBASE_PROJECT_ID", ""),
		FirebaseServiceAccount: getEnv("FIREBASE_SERVICE_ACCOUNT", ""),
		TestModeUnlockAll:      getBool("TEST_MODE_UNLOCK_ALL", false),

		SentryDSN:     getEnv("SENTRY_DSN", ""),
		SentryRelease: getEnv("SENTRY_RELEASE", ""),

		CORSAllowedOrigins: getList("CORS_ALLOWED_ORIGINS"),

		// Parola deneme saldırılarını yavaşlatacak kadar sıkı, gerçek
		// kullanıcıyı engellemeyecek kadar gevşek.
		AuthRateLimit:       getInt("AUTH_RATE_LIMIT", 10),
		AuthRateLimitWindow: getDuration("AUTH_RATE_LIMIT_WINDOW", time.Minute),
		APIRateLimit:        getInt("API_RATE_LIMIT", 120),
		APIRateLimitWindow:  getDuration("API_RATE_LIMIT_WINDOW", time.Minute),

		ReadTimeout:  getDuration("READ_TIMEOUT", 15*time.Second),
		WriteTimeout: getDuration("WRITE_TIMEOUT", 30*time.Second),
		IdleTimeout:  getDuration("IDLE_TIMEOUT", 60*time.Second),
		BodyLimit:    getInt("BODY_LIMIT_BYTES", 1<<20), // 1 MiB
	}

	if cfg.JWTSecret == "dev-secret-change-me" && cfg.AppEnv == "production" {
		log.Fatal("config: production'da JWT_SECRET ayarlanmalı")
	}

	return cfg
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
