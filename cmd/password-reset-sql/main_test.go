package main

import (
	"strings"
	"testing"

	"kelimeler-backend/internal/auth"
)

func TestTempPassword(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		p, err := tempPassword()
		if err != nil {
			t.Fatal(err)
		}
		if len(p) != tempPasswordLength {
			t.Fatalf("uzunluk %d", len(p))
		}
		if strings.ContainsAny(p, "0O1lI") {
			t.Fatalf("karışabilen karakter: %q", p)
		}
		if seen[p] {
			t.Fatalf("tekrar eden parola: %q", p)
		}
		seen[p] = true
	}
}

func TestNormalizeEmail(t *testing.T) {
	for _, ok := range []string{"ayse@example.com", " Ayse.Y@Example.com "} {
		if _, err := normalizeEmail(ok); err != nil {
			t.Errorf("%q reddedildi: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "ayse", "Ayşe <a@b.co>", "a'b@c.co", "a@b.co; DROP TABLE users", "a\\b@c.co"} {
		if _, err := normalizeEmail(bad); err == nil {
			t.Errorf("%q kabul edildi", bad)
		}
	}
}

func TestResetSQL(t *testing.T) {
	hash, err := auth.HashPassword("GeciciParola9")
	if err != nil {
		t.Fatal(err)
	}
	sql := resetSQL("ayse@example.com", hash)
	for _, want := range []string{
		"-v ON_ERROR_STOP=1",
		"BEGIN;",
		"UPDATE auth_identities SET password_hash = '" + hash + "'",
		"provider = 'password'",
		"DELETE FROM refresh_tokens",
		"token_version = token_version + 1",
		"COMMIT;",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL'de yok: %q\n%s", want, sql)
		}
	}
	if strings.Count(sql, "'ayse@example.com'") != 3 {
		t.Errorf("e-posta üç sorguda da olmalı:\n%s", sql)
	}
	if !auth.VerifyPassword(hash, "GeciciParola9") {
		t.Fatal("özet parolayı doğrulamıyor")
	}
}

func TestSQLString(t *testing.T) {
	if got := sqlString("a'b"); got != "'a''b'" {
		t.Fatalf("got %s", got)
	}
}
