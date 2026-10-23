package services

import (
	"testing"
	"time"
)

// gunler, verilen offsetlerdeki (bugünden geriye) günleri işaretli bir küme yapar.
func gunler(now time.Time, offsets ...int) map[string]bool {
	m := map[string]bool{}
	for _, o := range offsets {
		m[FormatDate(now.AddDate(0, 0, -o))] = true
	}
	return m
}

func TestFormatDate(t *testing.T) {
	d := time.Date(2026, 8, 23, 15, 4, 5, 0, time.UTC)
	if got := FormatDate(d); got != "2026-08-23" {
		t.Errorf("FormatDate = %q, beklenen \"2026-08-23\"", got)
	}
}

func TestCalculateStreak(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name    string
		offsets []int
		want    int
	}{
		{
			name:    "bugun giris yoksa streak 0",
			offsets: []int{1, 2, 3},
			want:    0,
		},
		{
			name:    "yalnizca bugun",
			offsets: []int{0},
			want:    1,
		},
		{
			name:    "kesintisiz uc gun",
			offsets: []int{0, 1, 2},
			want:    3,
		},
		{
			name:    "bosluk streaki keser",
			offsets: []int{0, 1, 3, 4}, // 2 gün önce eksik
			want:    2,
		},
		{
			name:    "hic giris yok",
			offsets: nil,
			want:    0,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CalculateStreak(gunler(now, c.offsets...), now); got != c.want {
				t.Errorf("CalculateStreak = %d, beklenen %d", got, c.want)
			}
		})
	}
}

func TestCalculateStreak_365GunUstSinir(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

	// Kesintisiz 400 gün: döngü 0..365 aralığında olduğu için 366'da durur.
	all := map[string]bool{}
	for i := 0; i < 400; i++ {
		all[FormatDate(now.AddDate(0, 0, -i))] = true
	}

	if got := CalculateStreak(all, now); got != 366 {
		t.Errorf("CalculateStreak = %d, beklenen 366 (0..365 dahil üst sınır)", got)
	}
}

func TestUsageDate_GunSiniriKullanimKonumunda(t *testing.T) {
	kullanimKonumu(t, "Europe/Istanbul")
	cases := []struct {
		name string
		at   time.Time
		want string
	}{
		{"UTC 20:59 = TR 23:59, ayni gun", time.Date(2026, 10, 22, 20, 59, 0, 0, time.UTC), "2026-10-22"},
		{"UTC 21:00 = TR 00:00, ertesi gun", time.Date(2026, 10, 22, 21, 0, 0, 0, time.UTC), "2026-10-23"},
		{"UTC 23:30 = TR 02:30, ertesi gun", time.Date(2026, 10, 22, 23, 30, 0, 0, time.UTC), "2026-10-23"},
		{"UTC 00:30 = TR 03:30, ayni gun", time.Date(2026, 10, 23, 0, 30, 0, 0, time.UTC), "2026-10-23"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := UsageDate(c.at); got != c.want {
				t.Errorf("UsageDate(%s) = %q, beklenen %q", c.at.Format(time.RFC3339), got, c.want)
			}
		})
	}
}

func TestUsageDateDaysAgo_AySonunuGecer(t *testing.T) {
	kullanimKonumu(t, "Europe/Istanbul")
	// TR 1 Kasım 01:00 (UTC 31 Ekim 22:00): bugün 1 Kasım, iki gün önce 30 Ekim.
	now := time.Date(2026, 10, 31, 22, 0, 0, 0, time.UTC)
	if got := UsageDateDaysAgo(now, 0); got != "2026-11-01" {
		t.Errorf("bugün = %q, beklenen 2026-11-01", got)
	}
	if got := UsageDateDaysAgo(now, 2); got != "2026-10-30" {
		t.Errorf("iki gün önce = %q, beklenen 2026-10-30", got)
	}
}

func TestCalculateStreak_GeceYarisindanSonraTRGunuSayilir(t *testing.T) {
	kullanimKonumu(t, "Europe/Istanbul")
	// UTC 23:30 = TR 02:30: bugün 23 Ekim. Önceki gün sınırı (UTC) bugünü
	// 22 Ekim sayıyor ve 23'ünün girişini görmüyordu.
	now := time.Date(2026, 10, 22, 23, 30, 0, 0, time.UTC)
	logged := map[string]bool{"2026-10-23": true, "2026-10-22": true, "2026-10-21": true}
	if got := CalculateStreak(logged, now); got != 3 {
		t.Errorf("CalculateStreak = %d, beklenen 3", got)
	}

	// Bugünün (TR) girişi yoksa seri 0'dır, dünkü UTC günü girişli olsa bile.
	if got := CalculateStreak(map[string]bool{"2026-10-22": true}, now); got != 0 {
		t.Errorf("CalculateStreak = %d, beklenen 0", got)
	}
}

func TestLoginDays(t *testing.T) {
	kullanimKonumu(t, "Europe/Istanbul")
	at := func(s string) *time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return &v
	}
	cases := []struct {
		name string
		rows []LoginRecord
		want []string
	}{
		{
			name: "yeni kayit: login_date zaten TR gunu",
			rows: []LoginRecord{{LoginDate: "2026-10-23", CreatedAt: at("2026-10-22T22:30:00Z")}},
			want: []string{"2026-10-23"},
		},
		{
			name: "eski UTC kaydi: TR 01:30 girisi ertesi gunu de sayar",
			rows: []LoginRecord{{LoginDate: "2026-10-22", CreatedAt: at("2026-10-22T22:30:00Z")}},
			want: []string{"2026-10-22", "2026-10-23"},
		},
		{
			name: "eski UTC kaydi gunduz: tek gun",
			rows: []LoginRecord{{LoginDate: "2026-10-22", CreatedAt: at("2026-10-22T09:00:00Z")}},
			want: []string{"2026-10-22"},
		},
		{
			name: "created_at yok",
			rows: []LoginRecord{{LoginDate: "2026-10-22"}},
			want: []string{"2026-10-22"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := LoginDays(c.rows)
			if len(got) != len(c.want) {
				t.Fatalf("LoginDays = %v, beklenen %v", got, c.want)
			}
			for _, d := range c.want {
				if !got[d] {
					t.Errorf("LoginDays = %v, %s eksik", got, d)
				}
			}
		})
	}
}
