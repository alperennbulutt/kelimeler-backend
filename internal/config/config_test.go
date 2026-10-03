package config

import "testing"

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
	t.Setenv("APP_ENV", "test")
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
	t.Setenv("APP_ENV", "test")
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
