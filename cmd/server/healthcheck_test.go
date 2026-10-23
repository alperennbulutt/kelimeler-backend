package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthcheck_CikisKodu(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   int
	}{
		{"saglikli", http.StatusOK, 0},
		{"veritabani yok", http.StatusServiceUnavailable, 1},
		{"hiz siniri", http.StatusTooManyRequests, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/health" {
					http.NotFound(w, r)
					return
				}
				w.WriteHeader(c.status)
			}))
			defer srv.Close()

			if got := healthcheck(srv.URL + "/api/v1/health"); got != c.want {
				t.Errorf("healthcheck = %d, beklenen %d", got, c.want)
			}
		})
	}

	t.Run("sunucu calismiyor", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		url := srv.URL
		srv.Close()
		if got := healthcheck(url + "/api/v1/health"); got != 1 {
			t.Errorf("healthcheck = %d, beklenen 1", got)
		}
	})
}
