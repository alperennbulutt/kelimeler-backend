package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
)

// yavasSunucu, /yavas isteği geldiğinde başladi kanalını kapatan ve bekle
// kadar sonra yanıt veren bir uygulamayı serve ile rastgele bir portta başlatır.
func yavasSunucu(t *testing.T, bekle, timeout time.Duration) (url string, basladi <-chan struct{}, cancel context.CancelFunc, sonuc <-chan error) {
	t.Helper()

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	b := make(chan struct{})
	app.Get("/yavas", func(c *fiber.Ctx) error {
		close(b)
		time.Sleep(bekle)
		return c.SendString("bitti")
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("dinlenemedi: %v", err)
	}
	ctx, cancelFn := context.WithCancel(context.Background())
	s := make(chan error, 1)
	go func() { s <- serve(ctx, app, ln, timeout) }()
	t.Cleanup(cancelFn)

	return "http://" + ln.Addr().String(), b, cancelFn, s
}

type yanit struct {
	status int
	govde  string
	err    error
}

func getAsync(url string) <-chan yanit {
	out := make(chan yanit, 1)
	go func() {
		resp, err := http.Get(url)
		if err != nil {
			out <- yanit{err: err}
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		out <- yanit{status: resp.StatusCode, govde: string(b)}
	}()
	return out
}

// Coolify redeploy'da SIGTERM gönderir; o anda işlenen istek yarıda
// kesilmemeli, yanıtı istemciye ulaşmalı.
func TestServe_KapanmaSinyalindeSurenIstekTamamlanir(t *testing.T) {
	url, basladi, cancel, sonuc := yavasSunucu(t, 300*time.Millisecond, 5*time.Second)

	istek := getAsync(url + "/yavas")
	select {
	case <-basladi:
	case <-time.After(5 * time.Second):
		t.Fatal("istek handler'a ulaşmadı")
	}
	cancel() // SIGTERM karşılığı

	y := <-istek
	if y.err != nil {
		t.Fatalf("süren istek kapanmada kesildi: %v", y.err)
	}
	if y.status != http.StatusOK || y.govde != "bitti" {
		t.Errorf("süren istek: status = %d, gövde = %q; beklenen 200 \"bitti\"", y.status, y.govde)
	}

	select {
	case err := <-sonuc:
		if err != nil {
			t.Errorf("serve = %v, beklenen nil (düzgün kapanma)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve kapanmadan dönmedi")
	}

	// Kapandıktan sonra yeni bağlantı kabul edilmez.
	if _, err := http.Get(url + "/yavas"); err == nil {
		t.Error("kapanmış sunucu yeni isteği kabul etti")
	}
}

// Süre dolarsa serve beklemeyi bırakır ve hatayla döner; Docker'ın SIGKILL'inden
// önce Sentry kuyruğu boşaltılabilsin.
func TestServe_KapanmaSuresiDolarsaHataDoner(t *testing.T) {
	url, basladi, cancel, sonuc := yavasSunucu(t, 2*time.Second, 100*time.Millisecond)

	_ = getAsync(url + "/yavas")
	select {
	case <-basladi:
	case <-time.After(5 * time.Second):
		t.Fatal("istek handler'a ulaşmadı")
	}
	cancel()

	select {
	case err := <-sonuc:
		if err == nil {
			t.Error("serve = nil, beklenen kapanma süresi hatası")
		}
	case <-time.After(time.Second):
		t.Fatal("serve kapanma süresi dolduktan sonra da bekliyor")
	}
}
