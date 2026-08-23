package services

import (
	"testing"
	"time"
)

func TestNeedsReset(t *testing.T) {
	now := time.Now()

	cases := []struct {
		name   string
		millis int64
		want   bool
	}{
		{
			// Hiç kayıt yoksa (yeni kullanıcı) reset gerekir.
			name:   "sifir degeri reset ister",
			millis: 0,
			want:   true,
		},
		{
			name:   "bugun yazilmissa reset istemez",
			millis: now.UnixMilli(),
			want:   false,
		},
		{
			name:   "bugunun basi reset istemez",
			millis: time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 1, 0, now.Location()).UnixMilli(),
			want:   false,
		},
		{
			name:   "dun yazilmissa reset ister",
			millis: now.AddDate(0, 0, -1).UnixMilli(),
			want:   true,
		},
		{
			name:   "gecen yil ayni gun reset ister",
			millis: now.AddDate(-1, 0, 0).UnixMilli(),
			want:   true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NeedsReset(c.millis); got != c.want {
				t.Errorf("NeedsReset(%d) = %v, beklenen %v", c.millis, got, c.want)
			}
		})
	}
}

// Reklamsız geçiş sınırı para kazanma açısından kritiktir: sessizce
// gevşetilirse uçak modunda sınırsız ücretsiz kullanım mümkün olur.
func TestMaxNoFillPassesPerDay(t *testing.T) {
	if MaxNoFillPassesPerDay != 2 {
		t.Errorf("MaxNoFillPassesPerDay = %d, beklenen 2. "+
			"Bu sınır bilerek değiştirildiyse testi de güncelleyin.",
			MaxNoFillPassesPerDay)
	}
	if MaxNoFillPassesPerDay < 0 {
		t.Fatal("MaxNoFillPassesPerDay negatif olamaz")
	}
}
