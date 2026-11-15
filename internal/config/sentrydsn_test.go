package config

import (
	"strings"
	"testing"
)

func TestParseSentryDSN_Gecerli(t *testing.T) {
	for in, want := range map[string]SentryDSN{
		"https://edb7abc@o4512437762981888.ingest.us.sentry.io/4512437775106048": {
			PublicKey: "edb7abc", Host: "o4512437762981888.ingest.us.sentry.io", ProjectID: "4512437775106048",
		},
		" https://k@Sentry.Example.com:8443/a/b/42/ ": {
			PublicKey: "k", Host: "sentry.example.com:8443", PathPrefix: "/a/b", ProjectID: "42",
		},
	} {
		got, err := ParseSentryDSN(in)
		if err != nil {
			t.Errorf("ParseSentryDSN(%q) hata: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseSentryDSN(%q) = %+v, beklenen %+v", in, got, want)
		}
	}
}

func TestSentryDSN_EnvelopeURL(t *testing.T) {
	d, err := ParseSentryDSN("https://edb7abc@o1.ingest.us.sentry.io/4512437775106048")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := d.EnvelopeURL(), "https://o1.ingest.us.sentry.io/api/4512437775106048/envelope/"; got != want {
		t.Errorf("EnvelopeURL() = %q, beklenen %q", got, want)
	}
	d, _ = ParseSentryDSN("https://k@h.example/a/b/42")
	if got, want := d.EnvelopeURL(), "https://h.example/a/b/api/42/envelope/"; got != want {
		t.Errorf("EnvelopeURL() = %q, beklenen %q", got, want)
	}
}

func TestParseSentryDSN_Gecersiz(t *testing.T) {
	for _, in := range []string{
		"",
		"sentry",
		"http://k@o1.ingest.sentry.io/42",        // https değil
		"https://o1.ingest.sentry.io/42",         // anahtar yok
		"https://k:gizli@o1.ingest.sentry.io/42", // eski biçim
		"https://k@/42",                          // host yok
		"https://k@o1.ingest.sentry.io",          // proje yok
		"https://k@o1.ingest.sentry.io/",         // proje yok
		"https://k@o1.ingest.sentry.io/proje",    // proje sayı değil
		"https://k@o1.ingest.sentry.io/42?x=1",   // sorgu
		"https://k@o1.ingest.sentry.io/42#x",     // parça
	} {
		if d, err := ParseSentryDSN(in); err == nil {
			t.Errorf("ParseSentryDSN(%q) = %+v, hata bekleniyordu", in, d)
		}
	}
}

func TestFromEnv_SentryRelay(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("JWT_SECRET", strings.Repeat("x", MinJWTSecretBytes))

	t.Run("tanimsiz", func(t *testing.T) {
		t.Setenv("SENTRY_RELAY_DSN", "")
		cfg, err := FromEnv()
		if err != nil {
			t.Fatalf("FromEnv: %v", err)
		}
		if cfg.SentryRelay != nil {
			t.Errorf("SentryRelay = %+v, beklenen nil (röle kapalı)", cfg.SentryRelay)
		}
		for _, w := range cfg.Warnings() {
			if strings.Contains(w, "SENTRY_RELAY_DSN") {
				t.Errorf("tanımsızken uyarı olmamalı: %q", w)
			}
		}
	})

	t.Run("gecerli", func(t *testing.T) {
		t.Setenv("SENTRY_RELAY_DSN", " https://edb7abc@o4512437762981888.ingest.us.sentry.io/4512437775106048 ")
		cfg, err := FromEnv()
		if err != nil {
			t.Fatalf("FromEnv: %v", err)
		}
		want := SentryDSN{PublicKey: "edb7abc", Host: "o4512437762981888.ingest.us.sentry.io", ProjectID: "4512437775106048"}
		if cfg.SentryRelay == nil || *cfg.SentryRelay != want {
			t.Errorf("SentryRelay = %+v, beklenen %+v", cfg.SentryRelay, want)
		}
	})

	// Geçersiz değer açılışı durdurmaz: uyarı verilir ve röle kapalı kalır.
	for _, env := range []string{EnvProduction, EnvDevelopment} {
		t.Run("gecersiz "+env, func(t *testing.T) {
			t.Setenv("APP_ENV", env)
			t.Setenv("SENTRY_RELAY_DSN", "http://edb7abc@o1.ingest.us.sentry.io/proje")
			cfg, err := FromEnv()
			if err != nil {
				t.Fatalf("FromEnv geçersiz DSN'de açılışı durdurmamalı: %v", err)
			}
			if cfg.SentryRelay != nil {
				t.Errorf("SentryRelay = %+v, beklenen nil", cfg.SentryRelay)
			}
			var bulundu bool
			for _, w := range cfg.Warnings() {
				if strings.Contains(w, "SENTRY_RELAY_DSN") && strings.Contains(w, "kapalı") {
					bulundu = true
				}
			}
			if !bulundu {
				t.Errorf("Warnings() = %q, SENTRY_RELAY_DSN uyarısı bekleniyordu", cfg.Warnings())
			}
		})
	}
}
