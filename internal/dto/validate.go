package dto

import (
	"errors"
	"net/mail"
	"strings"
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

// NormalizeDisplayName, görünen adı kırpar ve uzunluğunu doğrular.
func NormalizeDisplayName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > MaxDisplayNameLength {
		return "", ErrDisplayNameLong
	}
	return name, nil
}
