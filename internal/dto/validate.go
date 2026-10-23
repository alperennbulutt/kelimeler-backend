package dto

import (
	"errors"
	"net/mail"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// MinPasswordLength, kaba kuvvet denemelerine karşı asgari uzunluk.
	MinPasswordLength = 8

	// MaxPasswordLength, bcrypt'in sert sınırıdır: 72 bayttan uzun parolalar
	// sessizce kırpılır, yani "uzun" bir parola aslında ilk 72 baytı kadar
	// güçlü olur. Sessiz kırpma yerine açıkça reddediyoruz.
	MaxPasswordLength = 72

	MaxEmailLength       = 254 // RFC 5321
	MaxDisplayNameLength = 60
)

var (
	ErrInvalidEmail    = errors.New("geçerli bir e-posta adresi girin")
	ErrPasswordTooWeak = errors.New("parola en az 8 karakter olmalı")
	ErrPasswordTooLong = errors.New("parola en fazla 72 karakter olabilir")
	ErrDisplayNameLong = errors.New("görünen ad en fazla 60 karakter olabilir")
)

// NormalizeEmail, e-postayı karşılaştırma ve saklama için tekilleştirir.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// ValidateEmail, adresin biçimsel olarak geçerli olduğunu doğrular.
func ValidateEmail(email string) error {
	if email == "" || len(email) > MaxEmailLength {
		return ErrInvalidEmail
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return ErrInvalidEmail
	}
	// mail.ParseAddress "a@b" gibi alan adı noktasız adresleri de kabul eder.
	at := strings.LastIndex(email, "@")
	domain := email[at+1:]
	if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return ErrInvalidEmail
	}
	return nil
}

// ValidatePassword, uzunluk sınırlarını uygular.
// Bayt cinsinden ölçeriz: bcrypt'in sınırı bayt bazlıdır ve Türkçe karakterler
// birden fazla bayt tutar.
func ValidatePassword(password string) error {
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return ErrPasswordTooWeak
	}
	if len(password) > MaxPasswordLength {
		return ErrPasswordTooLong
	}
	return nil
}

// cleanDisplayName, görünen addan görünmez ve yön değiştiren karakterleri
// atar, boşlukları tek boşluğa indirir ve kırpar.
//
// Kontrol karakterleri (satır sonu, sekme dahil) ve Unicode biçim karakterleri
// (U+202E gibi yön değiştiriciler, U+200B sıfır genişlikli boşluk, BOM) adın
// gösterildiği yerde metni ters çevirebilir ya da boş görünen adlar
// üretebilir. ZWJ/ZWNJ (U+200D/U+200C) korunur: emoji dizileri ve bazı
// yazı sistemleri onlara dayanır.
func cleanDisplayName(name string) string {
	name = strings.ToValidUTF8(name, "")
	var b strings.Builder
	b.Grow(len(name))
	space := false
	for _, r := range name {
		switch {
		case unicode.IsSpace(r):
			space = true
			continue
		case r == '\u200c' || r == '\u200d':
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r):
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

// NormalizeDisplayName, görünen adı temizler (bkz. cleanDisplayName) ve
// uzunluğunu doğrular. Kullanıcının kendi girdiği ad için: sınırı aşan ad
// reddedilir.
func NormalizeDisplayName(name string) (string, error) {
	name = cleanDisplayName(name)
	if utf8.RuneCountInString(name) > MaxDisplayNameLength {
		return "", ErrDisplayNameLong
	}
	return name, nil
}

// SanitizeDisplayName, görünen adı temizler ve MaxDisplayNameLength
// karaktere keser. Sosyal girişte (sağlayıcının ya da istemcinin verdiği ad)
// kullanılır: uzun bir ad girişi bozmamalı, yalnızca kısaltılmalıdır.
func SanitizeDisplayName(name string) string {
	name = cleanDisplayName(name)
	if utf8.RuneCountInString(name) <= MaxDisplayNameLength {
		return name
	}
	runes := []rune(name)[:MaxDisplayNameLength]
	return strings.TrimSpace(string(runes))
}
