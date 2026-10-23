package handlers

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// openLazy, bağlanmayı ilk sorguya bırakan bir gorm.DB açar.
func openLazy(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("gorm açılamadı: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := gdb.DB(); err == nil {
			sqlDB.Close()
		}
	})
	return gdb
}

func healthIstegi(t *testing.T, gdb *gorm.DB) (int, map[string]any, time.Duration) {
	t.Helper()
	app := routesApp(gdb, "development", sinirsiz)
	basla := time.Now()
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/api/v1/health", nil), 10_000)
	if err != nil {
		t.Fatalf("health isteği başarısız: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("health gövdesi JSON değil: %q", raw)
	}
	return resp.StatusCode, body, time.Since(basla)
}

// Eskiden hata yutuluyor ve DB yokken de 200 {"status":"ok","dictionaryWords":0} dönüyordu.
func TestHealth_VeritabaniYoksa503(t *testing.T) {
	// Port 1'de dinleyen yok: bağlantı hemen reddedilir.
	gdb := openLazy(t, "postgres://u:p@127.0.0.1:1/yok?sslmode=disable")

	status, body, _ := healthIstegi(t, gdb)
	if status != fiber.StatusServiceUnavailable {
		t.Fatalf("status = %d, beklenen 503 (body: %v)", status, body)
	}
	if body["status"] != "unavailable" || body["code"] != "db_unavailable" {
		t.Errorf("gövde = %v, beklenen status=unavailable code=db_unavailable", body)
	}
}

// Bağlantıyı kabul edip hiç yanıt vermeyen veritabanında probe asılı kalmaz.
func TestHealth_AsiliVeritabanindaZamanAsimiyla503(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("dinlenemedi: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { conn.Close() })
		}
	}()

	gdb := openLazy(t, "postgres://u:p@"+ln.Addr().String()+"/asili?sslmode=disable")

	status, body, sure := healthIstegi(t, gdb)
	if status != fiber.StatusServiceUnavailable {
		t.Fatalf("status = %d, beklenen 503 (body: %v)", status, body)
	}
	if sure > healthTimeout+2*time.Second {
		t.Errorf("health %s sürdü, beklenen ~%s içinde dönmesi", sure, healthTimeout)
	}
}

func TestHealth_VeritabaniVarsa200(t *testing.T) {
	gdb := testDB(t)

	status, body, _ := healthIstegi(t, gdb)
	if status != fiber.StatusOK || body["status"] != "ok" {
		t.Fatalf("status = %d, gövde = %v; beklenen 200 status=ok", status, body)
	}
	if _, ok := body["dictionaryWords"].(float64); !ok {
		t.Errorf("dictionaryWords alanı yok ya da sayı değil: %v", body)
	}
}
