package dto

import (
	"strings"
	"testing"
)

func TestNormalizeEmail(t *testing.T) {
	cases := map[string]string{
		"  Test@Example.COM ": "test@example.com",
		"user@example.com":    "user@example.com",
		"":                    "",
	}
	for in, want := range cases {
		if got := NormalizeEmail(in); got != want {
			t.Errorf("NormalizeEmail(%q) = %q, beklenen %q", in, got, want)
		}
	}
}

func TestValidateEmail(t *testing.T) {
	gecerli := []string{
		"user@example.com",
		"a.b+tag@alt.example.co.uk",
		"kullanici@ornek.com.tr",
	}
	for _, e := range gecerli {
		if err := ValidateEmail(e); err != nil {
			t.Errorf("ValidateEmail(%q) hata verdi: %v", e, err)
		}
	}

	gecersiz := []string{
		"",
		"duz-metin",
		"@example.com",
		"user@",
		"user@localhost",     // alan adında nokta yok
		"user@.example.com",  // nokta ile başlıyor
		"user@example.",      // nokta ile bitiyor
		"Ad Soyad <a@b.com>", // görünen adlı biçim kabul edilmemeli
		strings.Repeat("a", 250) + "@example.com", // 254 sınırını aşıyor
	}
	for _, e := range gecersiz {
		if err := ValidateEmail(e); err == nil {
			t.Errorf("ValidateEmail(%q) geçerli saydı, reddedilmeliydi", e)
		}
	}
}

func TestValidatePassword(t *testing.T) {
	if err := ValidatePassword("kisa"); err != ErrPasswordTooWeak {
		t.Errorf("kısa parola için ErrPasswordTooWeak beklenir, alınan: %v", err)
	}
	if err := ValidatePassword("1234567"); err != ErrPasswordTooWeak {
		t.Errorf("7 karakter reddedilmeli, alınan: %v", err)
	}
	if err := ValidatePassword("12345678"); err != nil {
		t.Errorf("8 karakter kabul edilmeli, alınan: %v", err)
	}

	// bcrypt 72 bayttan sonrasını sessizce kırpar; bunu reddediyoruz.
	if err := ValidatePassword(strings.Repeat("a", 73)); err != ErrPasswordTooLong {
		t.Errorf("73 bayt reddedilmeli, alınan: %v", err)
	}
	if err := ValidatePassword(strings.Repeat("a", 72)); err != nil {
		t.Errorf("72 bayt kabul edilmeli, alınan: %v", err)
	}
}

// Türkçe karakterler UTF-8'de 2 bayt tutar: uzunluk kontrolü karakter,
// bcrypt sınırı bayt bazlıdır. İkisinin karışmadığını doğrula.
func TestValidatePassword_TurkceKarakterler(t *testing.T) {
	// 8 karakter ama 16 bayt: karakter sayısı yeterli, kabul edilmeli.
	if err := ValidatePassword("şşşşşşşş"); err != nil {
		t.Errorf("8 Türkçe karakterli parola kabul edilmeli, alınan: %v", err)
	}

	// 40 karakter ama 80 bayt: bcrypt sınırını aşıyor, reddedilmeli.
	long := strings.Repeat("ş", 40)
	if len(long) <= MaxPasswordLength {
		t.Fatalf("test kurgusu bozuk: %d bayt", len(long))
	}
	if err := ValidatePassword(long); err != ErrPasswordTooLong {
		t.Errorf("80 baytlık parola reddedilmeli, alınan: %v", err)
	}
}

func TestNormalizeDisplayName(t *testing.T) {
	got, err := NormalizeDisplayName("  Kübra  ")
	if err != nil {
		t.Fatalf("beklenmeyen hata: %v", err)
	}
	if got != "Kübra" {
		t.Errorf("NormalizeDisplayName = %q, beklenen \"Kübra\"", got)
	}

	if _, err := NormalizeDisplayName(strings.Repeat("a", 61)); err != ErrDisplayNameLong {
		t.Errorf("61 karakter reddedilmeli, alınan: %v", err)
	}
	if _, err := NormalizeDisplayName(strings.Repeat("a", 60)); err != nil {
		t.Errorf("60 karakter kabul edilmeli, alınan: %v", err)
	}
}
