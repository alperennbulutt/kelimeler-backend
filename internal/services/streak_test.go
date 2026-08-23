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
