package config

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func TestGetPositiveInt(t *testing.T) {
	const key = "KELIMELER_TEST_POSITIVE_INT"

	cases := []struct {
		name  string
		value string
		want  int
	}{
		{"tanimsiz", "", 600},
		{"gecerli deger", "25", 25},
		{"bir", "1", 1},
		{"sifir", "0", 600},
		{"negatif", "-5", 600},
		{"sayi degil", "abc", 600},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(key, c.value)
			if got := getPositiveInt(key, 600); got != c.want {
				t.Errorf("getPositiveInt(%q) = %d, beklenen %d", c.value, got, c.want)
			}
		})
	}
}

// Fiber'ın limiter'ı Max <= 0 değerini dakikada 5 isteğe çevirir; yapılandırma
// bu değerleri limiter'a hiç ulaştırmamalı.
func TestLoad_SifirVeNegatifHizSinirlariVarsayilanaDoner(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("API_RATE_LIMIT", "0")
	t.Setenv("AUTH_RATE_LIMIT", "-1")

	cfg := Load()

	if cfg.APIRateLimit != 600 {
		t.Errorf("APIRateLimit = %d, beklenen 600", cfg.APIRateLimit)
	}
	if cfg.AuthRateLimit != 10 {
		t.Errorf("AuthRateLimit = %d, beklenen 10", cfg.AuthRateLimit)
	}
}

func TestLoad_GecerliHizSinirlariKorunur(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("API_RATE_LIMIT", "1200")
	t.Setenv("AUTH_RATE_LIMIT", "20")

	cfg := Load()

	if cfg.APIRateLimit != 1200 {
		t.Errorf("APIRateLimit = %d, beklenen 1200", cfg.APIRateLimit)
	}
	if cfg.AuthRateLimit != 20 {
		t.Errorf("AuthRateLimit = %d, beklenen 20", cfg.AuthRateLimit)
	}
}

const gucluSecret = "8cQx1v3V0aG9mZ2hpamtsbW5vcHFyc3R1dnd4eXo="

func TestFromEnv_OrtamVeJWTDogrulamasi(t *testing.T) {
	cases := []struct {
		name    string
		appEnv  string
		secret  string // "" ise JWT_SECRET tanımsız
		wantErr bool
	}{
		{"development varsayilan secret", "development", "", false},
		{"development kisa secret", "development", "kisa", false},
		{"production guclu secret", "production", gucluSecret, false},
		{"APP_ENV tanimsiz", "", gucluSecret, true},
		{"APP_ENV buyuk harfli", "Production", gucluSecret, true},
		{"APP_ENV kisaltma", "prod", gucluSecret, true},
		{"APP_ENV test", "test", gucluSecret, true},
		{"APP_ENV bosluklu", "production ", gucluSecret, true},
		{"production secret tanimsiz (kod varsayilani)", "production", "", true},
		{"production kisa secret", "production", "0123456789abcdef0123456789abcde", true}, // 31 bayt
		{"production tam 32 bayt", "production", "0123456789abcdef0123456789abcdef", false},
		{"production .env.example yer tutucusu", "production", "change-me-to-a-long-random-string", true},
		{"production .env.production.example yer tutucusu", "production", "BURAYA_UZUN_RASTGELE_DEGER_KOYUN", true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("APP_ENV", c.appEnv)
			t.Setenv("JWT_SECRET", c.secret)

			cfg, err := FromEnv()
			if c.wantErr {
				if err == nil {
					t.Fatalf("FromEnv() hata vermedi, beklenen açılışın reddedilmesi (cfg.AppEnv=%q)", cfg.AppEnv)
				}
				return
			}
			if err != nil {
				t.Fatalf("FromEnv() = %v, beklenen hata yok", err)
			}
			if cfg.AppEnv != c.appEnv {
				t.Errorf("AppEnv = %q, beklenen %q", cfg.AppEnv, c.appEnv)
			}
		})
	}
}

// Örnek dosyalardaki JWT_SECRET değerleri repoda açıkça durur; biri listeye
// eklenmeden değiştirilirse production'da kabul edilmeye başlar.
func TestPlaceholderJWTSecrets_OrnekDosyalarlaUyumlu(t *testing.T) {
	for _, dosya := range []string{"../../.env.example", "../../.env.production.example"} {
		veri, err := os.ReadFile(dosya)
		if err != nil {
			t.Fatalf("%s okunamadı: %v", dosya, err)
		}
		bulundu := false
		for _, satir := range strings.Split(string(veri), "\n") {
			deger, ok := strings.CutPrefix(strings.TrimSpace(satir), "JWT_SECRET=")
			if !ok {
				continue
			}
			bulundu = true
			if !slices.Contains(placeholderJWTSecrets, deger) {
				t.Errorf("%s: JWT_SECRET=%q placeholderJWTSecrets listesinde yok", dosya, deger)
			}
		}
		if !bulundu {
			t.Errorf("%s: JWT_SECRET satırı bulunamadı", dosya)
		}
	}
}

func TestWarnings(t *testing.T) {
	tamApple := Config{AppleTeamID: "T", AppleKeyID: "K", ApplePrivateKey: "P", GoogleClientIDs: []string{"g"}}

	cases := []struct {
		name    string
		mutate  func(c *Config)
		wantSub []string // her biri bir uyarıda geçmeli
		wantLen int
	}{
		{"production eksiksiz", func(c *Config) { c.AppEnv = EnvProduction }, nil, 0},
		{"development eksikleri uyarmaz", func(c *Config) {
			c.AppEnv = EnvDevelopment
			c.AppleKeyID = ""
			c.GoogleClientIDs = nil
			c.TestModeUnlockAll = true
		}, nil, 0},
		{"production Apple anahtari eksik", func(c *Config) {
			c.AppEnv = EnvProduction
			c.ApplePrivateKey = ""
		}, []string{"APPLE_PRIVATE_KEY"}, 1},
		{"production Google ve test modu", func(c *Config) {
			c.AppEnv = EnvProduction
			c.GoogleClientIDs = nil
			c.TestModeUnlockAll = true
		}, []string{"GOOGLE_CLIENT_IDS", "TEST_MODE_UNLOCK_ALL"}, 2},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := tamApple
			c.mutate(&cfg)
			got := cfg.Warnings()
			if len(got) != c.wantLen {
				t.Fatalf("Warnings() = %q, beklenen %d uyarı", got, c.wantLen)
			}
			hepsi := strings.Join(got, "\n")
			for _, sub := range c.wantSub {
				if !strings.Contains(hepsi, sub) {
					t.Errorf("Warnings() = %q, %q geçmiyor", got, sub)
				}
			}
		})
	}
}

func TestFromEnv_GateEnforcement(t *testing.T) {
	cases := []struct {
		value   string
		want    GateEnforcement
		wantErr bool
	}{
		{"", GateLog, false}, // varsayılan
		{"off", GateOff, false},
		{"log", GateLog, false},
		{"enforce", GateEnforce, false},
		{"ENFORCE", "", true},
		{"on", "", true},
		{"true", "", true},
	}

	for _, c := range cases {
		t.Run("deger="+c.value, func(t *testing.T) {
			t.Setenv("APP_ENV", "development")
			t.Setenv("GATE_ENFORCEMENT", c.value)

			cfg, err := FromEnv()
			if c.wantErr {
				if err == nil {
					t.Fatalf("FromEnv() hata vermedi, GateEnforcement = %q", cfg.GateEnforcement)
				}
				return
			}
			if err != nil {
				t.Fatalf("FromEnv() = %v", err)
			}
			if cfg.GateEnforcement != c.want {
				t.Errorf("GateEnforcement = %q, beklenen %q", cfg.GateEnforcement, c.want)
			}
		})
	}
}
