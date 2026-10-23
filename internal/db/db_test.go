package db

import (
	"testing"
	"time"
)

func TestWithStatementTimeout(t *testing.T) {
	cases := []struct {
		name string
		dsn  string
		d    time.Duration
		want string
	}{
		{"kapali", "postgres://u:p@h:5432/db", 0, "postgres://u:p@h:5432/db"},
		{"negatif kapali", "postgres://u:p@h:5432/db", -time.Second, "postgres://u:p@h:5432/db"},
		{"sorgusuz URL", "postgres://u:p@h:5432/db", 10 * time.Second, "postgres://u:p@h:5432/db?statement_timeout=10000"},
		{"sorgulu URL", "postgres://u:p@h:5432/db?sslmode=disable", 1500 * time.Millisecond,
			"postgres://u:p@h:5432/db?sslmode=disable&statement_timeout=1500"},
		{"postgresql semasi", "postgresql://h/db", time.Second, "postgresql://h/db?statement_timeout=1000"},
		{"milisaniye alti yukari yuvarlanir", "postgres://h/db", time.Microsecond, "postgres://h/db?statement_timeout=1"},
		{"DSN'deki deger korunur", "postgres://h/db?statement_timeout=300", 10 * time.Second, "postgres://h/db?statement_timeout=300"},
		{"options ile verilen korunur", "postgres://h/db?options=-c%20statement_timeout%3D300", 10 * time.Second,
			"postgres://h/db?options=-c%20statement_timeout%3D300"},
		{"anahtar=deger bicimi", "host=h dbname=db sslmode=disable", 2 * time.Second, "host=h dbname=db sslmode=disable statement_timeout=2000"},
		{"anahtar=deger mevcut", "host=h statement_timeout=5", 2 * time.Second, "host=h statement_timeout=5"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := withStatementTimeout(c.dsn, c.d)
			if err != nil {
				t.Fatalf("hata: %v", err)
			}
			if got != c.want {
				t.Errorf("withStatementTimeout(%q, %s) = %q, beklenen %q", c.dsn, c.d, got, c.want)
			}
		})
	}
}

func TestWithStatementTimeout_GecersizURLParolayiSizdirmaz(t *testing.T) {
	_, err := withStatementTimeout("postgres://u:gizli%zz@h/db", time.Second)
	if err == nil {
		t.Fatal("geçersiz URL kabul edildi")
	}
	if got := err.Error(); got != "DATABASE_URL ayrıştırılamadı" {
		t.Errorf("hata = %q; DSN içeriği hataya girmemeli", got)
	}
}
