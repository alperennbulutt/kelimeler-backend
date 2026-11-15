package config

import (
	"errors"
	"net/url"
	"strings"
)

// SentryDSN, bir Sentry DSN'inin röle için gereken parçalarıdır:
//
//	https://PublicKey@Host/PathPrefix/ProjectID
//
// Sentry'nin bulut DSN'lerinde PathPrefix boştur.
type SentryDSN struct {
	PublicKey  string
	Host       string // gerekiyorsa port dahil, küçük harfle
	PathPrefix string // "" ya da "/a/b" (sonda "/" olmadan)
	ProjectID  string // yalnızca rakamlar
}

// EnvelopeURL, Sentry'nin envelope alım adresidir:
// https://Host/PathPrefix/api/ProjectID/envelope/.
func (d SentryDSN) EnvelopeURL() string {
	return "https://" + d.Host + d.PathPrefix + "/api/" + d.ProjectID + "/envelope/"
}

// ParseSentryDSN, https şemalı bir Sentry DSN'ini ayrıştırır. Açık anahtar,
// host ve sayısal proje numarası zorunludur; gizli anahtar (eski DSN biçimi),
// sorgu ve parça kabul edilmez.
func ParseSentryDSN(raw string) (SentryDSN, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return SentryDSN{}, errors.New("adres ayrıştırılamadı")
	}
	if u.Scheme != "https" {
		return SentryDSN{}, errors.New("şema https olmalı")
	}
	if u.User == nil || u.User.Username() == "" {
		return SentryDSN{}, errors.New("açık anahtar (KEY@) eksik")
	}
	if _, hasSecret := u.User.Password(); hasSecret {
		return SentryDSN{}, errors.New("gizli anahtarlı eski DSN biçimi desteklenmiyor")
	}
	if u.Host == "" {
		return SentryDSN{}, errors.New("host eksik")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return SentryDSN{}, errors.New("sorgu ya da parça içermemeli")
	}
	path := strings.TrimSuffix(u.Path, "/")
	i := strings.LastIndex(path, "/")
	if i < 0 {
		return SentryDSN{}, errors.New("proje numarası eksik")
	}
	project := path[i+1:]
	if !isDigits(project) {
		return SentryDSN{}, errors.New("proje numarası sayı olmalı")
	}
	return SentryDSN{
		PublicKey:  u.User.Username(),
		Host:       strings.ToLower(u.Host),
		PathPrefix: path[:i],
		ProjectID:  project,
	}, nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
