package config

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"
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

func TestFromEnv_TrustedProxies(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		want    []string
		wantErr bool
	}{
		{"varsayilan ozel aglar", "", DefaultTrustedProxies, false},
		{"CIDR ve IP", "10.0.1.0/24, 172.18.0.5", []string{"10.0.1.0/24", "172.18.0.5"}, false},
		{"IPv6", "fd00::/8", []string{"fd00::/8"}, false},
		{"gecersiz", "10.0.0.0/8,traefik", nil, true},
		{"gecersiz CIDR", "10.0.0.0/33", nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("APP_ENV", "development")
			t.Setenv("TRUSTED_PROXIES", c.value)
			cfg, err := FromEnv()
			if c.wantErr {
				if err == nil {
					t.Fatalf("FromEnv() hata vermedi, TrustedProxies = %q", cfg.TrustedProxies)
				}
				return
			}
			if err != nil {
				t.Fatalf("FromEnv() = %v", err)
			}
			if !slices.Equal(cfg.TrustedProxies, c.want) {
				t.Errorf("TrustedProxies = %q, beklenen %q", cfg.TrustedProxies, c.want)
			}
		})
	}
}

func TestFromEnv_GuestRefreshTokenTTL(t *testing.T) {
	cases := []struct {
		value   string
		want    time.Duration
		wantErr bool
	}{
		{"", DefaultGuestRefreshTokenTTL, false}, // varsayılan: 1 yıl
		{"4380h", 4380 * time.Hour, false},
		{"bozuk", DefaultGuestRefreshTokenTTL, false}, // ayrıştırılamayan süre varsayılana döner
		{"0s", 0, true},
		{"-1h", 0, true},
	}
	for _, c := range cases {
		t.Run("deger="+c.value, func(t *testing.T) {
			t.Setenv("APP_ENV", "development")
			t.Setenv("GUEST_REFRESH_TOKEN_TTL", c.value)

			cfg, err := FromEnv()
			if c.wantErr {
				if err == nil {
					t.Fatalf("FromEnv() hata vermedi, GuestRefreshTokenTTL = %s", cfg.GuestRefreshTokenTTL)
				}
				return
			}
			if err != nil {
				t.Fatalf("FromEnv() = %v", err)
			}
			if cfg.GuestRefreshTokenTTL != c.want {
				t.Errorf("GuestRefreshTokenTTL = %s, beklenen %s", cfg.GuestRefreshTokenTTL, c.want)
			}
		})
	}
}

// Sürüm ayarlarını temizler: geliştiricinin ortamındaki değerler testi bozmasın.
func surumAyarlariniTemizle(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"MIN_APP_VERSION_IOS", "MIN_APP_VERSION_ANDROID",
		"LATEST_APP_VERSION_IOS", "LATEST_APP_VERSION_ANDROID",
		"APP_STORE_URL_IOS", "APP_STORE_URL_ANDROID",
	} {
		t.Setenv(k, "")
	}
}

func TestFromEnv_SurumAyarlariVarsayilan(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	surumAyarlariniTemizle(t)

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	p := cfg.AppVersions
	if !p.IOS.Min.IsZero() || !p.IOS.Latest.IsZero() || !p.Android.Min.IsZero() || !p.Android.Latest.IsZero() {
		t.Errorf("ayar yokken sınır var: %+v", p)
	}
	if p.IOS.StoreURL != "" {
		t.Errorf("iOS mağaza adresi = %q, beklenen boş", p.IOS.StoreURL)
	}
	if p.Android.StoreURL != DefaultAppStoreURLAndroid {
		t.Errorf("Android mağaza adresi = %q, beklenen varsayılan", p.Android.StoreURL)
	}
	if w := cfg.Warnings(); len(w) != 0 {
		t.Errorf("Warnings() = %q, beklenen yok", w)
	}
}

func TestFromEnv_SurumAyarlariOkunur(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	surumAyarlariniTemizle(t)
	t.Setenv("MIN_APP_VERSION_IOS", "1.0.0")
	t.Setenv("LATEST_APP_VERSION_IOS", " 1.1.0 ")
	t.Setenv("MIN_APP_VERSION_ANDROID", "1.0.0+3")
	t.Setenv("LATEST_APP_VERSION_ANDROID", "1.1.0")
	t.Setenv("APP_STORE_URL_IOS", "https://apps.apple.com/app/id123")
	t.Setenv("APP_STORE_URL_ANDROID", "https://play.google.com/store/apps/details?id=baska")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	p := cfg.AppVersions
	for ad, c := range map[string][2]string{
		"ios min":        {p.IOS.Min.Raw, "1.0.0"},
		"ios latest":     {p.IOS.Latest.Raw, "1.1.0"},
		"android min":    {p.Android.Min.Raw, "1.0.0+3"},
		"android latest": {p.Android.Latest.Raw, "1.1.0"},
		"ios url":        {p.IOS.StoreURL, "https://apps.apple.com/app/id123"},
		"android url":    {p.Android.StoreURL, "https://play.google.com/store/apps/details?id=baska"},
	} {
		if c[0] != c[1] {
			t.Errorf("%s = %q, beklenen %q", ad, c[0], c[1])
		}
	}
	if w := cfg.Warnings(); len(w) != 0 {
		t.Errorf("Warnings() = %q, beklenen yok", w)
	}
}

// Geçersiz değer açılışı durdurmaz: uyarılır (development'ta da) ve yok sayılır.
func TestFromEnv_GecersizSurumAyarlariUyarilirVeYokSayilir(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	surumAyarlariniTemizle(t)
	t.Setenv("MIN_APP_VERSION_IOS", "v1.2")
	t.Setenv("LATEST_APP_VERSION_ANDROID", "1.2.0-beta")
	t.Setenv("MIN_APP_VERSION_ANDROID", "1.0.0")
	t.Setenv("APP_STORE_URL_IOS", "apps.apple.com/app/id123")
	t.Setenv("APP_STORE_URL_ANDROID", "javascript:alert(1)")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("geçersiz sürüm ayarı açılışı durdurdu: %v", err)
	}
	p := cfg.AppVersions
	if !p.IOS.Min.IsZero() || !p.Android.Latest.IsZero() {
		t.Errorf("geçersiz değer yok sayılmadı: %+v", p)
	}
	if p.Android.Min.Raw != "1.0.0" {
		t.Errorf("geçerli değer kayboldu: android min = %q", p.Android.Min.Raw)
	}
	if p.IOS.StoreURL != "" || p.Android.StoreURL != DefaultAppStoreURLAndroid {
		t.Errorf("geçersiz adres yok sayılmadı: ios=%q android=%q", p.IOS.StoreURL, p.Android.StoreURL)
	}

	w := cfg.Warnings()
	if len(w) != 4 {
		t.Fatalf("Warnings() = %q, beklenen 4 uyarı", w)
	}
	hepsi := strings.Join(w, "\n")
	for _, sub := range []string{"MIN_APP_VERSION_IOS", "LATEST_APP_VERSION_ANDROID", "APP_STORE_URL_IOS", "APP_STORE_URL_ANDROID"} {
		if !strings.Contains(hepsi, sub) {
			t.Errorf("Warnings() = %q, %q geçmiyor", w, sub)
		}
	}
}

func TestFromEnv_LatestMindenDusukseUyarilir(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	surumAyarlariniTemizle(t)
	t.Setenv("MIN_APP_VERSION_ANDROID", "1.2.0")
	t.Setenv("LATEST_APP_VERSION_ANDROID", "1.1.0")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if w := cfg.Warnings(); len(w) != 1 || !strings.Contains(w[0], "LATEST_APP_VERSION_ANDROID") {
		t.Errorf("Warnings() = %q, beklenen LATEST < MIN uyarısı", w)
	}
	// İki değer de korunur: en düşük sürüm yine uygulanır.
	if cfg.AppVersions.Android.Min.Raw != "1.2.0" || cfg.AppVersions.Android.Latest.Raw != "1.1.0" {
		t.Errorf("değerler değişti: %+v", cfg.AppVersions.Android)
	}
}

func TestWarnings_ProductiondaIOSMagazaAdresiEksik(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("JWT_SECRET", gucluSecret)
	t.Setenv("APPLE_TEAM_ID", "T")
	t.Setenv("APPLE_KEY_ID", "K")
	t.Setenv("APPLE_PRIVATE_KEY", "P")
	t.Setenv("GOOGLE_CLIENT_IDS", "g")
	t.Setenv("TEST_MODE_UNLOCK_ALL", "false")
	surumAyarlariniTemizle(t)
	t.Setenv("MIN_APP_VERSION_IOS", "1.0.0")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if w := cfg.Warnings(); len(w) != 1 || !strings.Contains(w[0], "APP_STORE_URL_IOS") {
		t.Errorf("Warnings() = %q, beklenen APP_STORE_URL_IOS uyarısı", w)
	}

	t.Setenv("APP_STORE_URL_IOS", "https://apps.apple.com/app/id123")
	cfg, err = FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if w := cfg.Warnings(); len(w) != 0 {
		t.Errorf("Warnings() = %q, beklenen yok", w)
	}
}
