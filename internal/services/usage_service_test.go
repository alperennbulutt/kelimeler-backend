package services

import (
	"reflect"
	"testing"
	"time"
)

// kullanimKonumu, testi verilen kullanım konumuna sabitler ve bitince eski
// konumu geri yükler; böylece sonuç makinenin saat dilimine bağlı kalmaz.
func kullanimKonumu(t *testing.T, name string) {
	t.Helper()

	onceki := UsageLocation()
	if err := SetUsageLocation(name); err != nil {
		t.Fatalf("SetUsageLocation(%q) başarısız: %v", name, err)
	}
	t.Cleanup(func() { usageLocation = onceki })
}

func utc(t *testing.T, s string) time.Time {
	t.Helper()

	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("zaman çözülemedi %q: %v", s, err)
	}
	return parsed
}

func TestUsageDayStart_IstanbulGeceYarisi(t *testing.T) {
	kullanimKonumu(t, "Europe/Istanbul")

	cases := []struct {
		name string
		now  string
		want string
	}{
		{
			// 23:59:59 TSİ: gün hâlâ 3 Ekim, başı bir önceki UTC gününe düşer.
			name: "gece yarisindan bir saniye once onceki gun",
			now:  "2026-10-03T20:59:59Z",
			want: "2026-10-02T21:00:00Z",
		},
		{
			name: "gece yarisi yeni gunun basi",
			now:  "2026-10-03T21:00:00Z",
			want: "2026-10-03T21:00:00Z",
		},
		{
			// 03:00 TSİ = 00:00 UTC: UTC günü döner ama kullanım günü dönmez.
			name: "utc gun donumu kullanim gununu degistirmez",
			now:  "2026-10-04T00:00:00Z",
			want: "2026-10-03T21:00:00Z",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := UsageDayStart(utc(t, c.now))
			if want := utc(t, c.want); !got.Equal(want) {
				t.Errorf("UsageDayStart(%s) = %s, beklenen %s",
					c.now, got.UTC().Format(time.RFC3339), c.want)
			}
		})
	}
}

func TestNeedsResetAt(t *testing.T) {
	kullanimKonumu(t, "Europe/Istanbul")

	// 4 Ekim 2026 13:00 TSİ; kullanım günü 3 Ekim 21:00 UTC'de başladı.
	now := utc(t, "2026-10-04T10:00:00Z")
	gunBasi := utc(t, "2026-10-03T21:00:00Z").UnixMilli()

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
			name:   "gun basindan bir ms once reset ister",
			millis: gunBasi - 1,
			want:   true,
		},
		{
			name:   "gun basi reset istemez",
			millis: gunBasi,
			want:   false,
		},
		{
			name:   "simdi yazilmissa reset istemez",
			millis: now.UnixMilli(),
			want:   false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NeedsResetAt(c.millis, now); got != c.want {
				t.Errorf("NeedsResetAt(%d) = %v, beklenen %v", c.millis, got, c.want)
			}
		})
	}
}

func TestNeedsReset(t *testing.T) {
	now := time.Now()

	cases := []struct {
		name   string
		millis int64
		want   bool
	}{
		{
			name:   "sifir degeri reset ister",
			millis: 0,
			want:   true,
		},
		{
			name:   "simdi yazilmissa reset istemez",
			millis: now.UnixMilli(),
			want:   false,
		},
		{
			// Gün başı makinenin değil kullanım konumunun gece yarısıdır.
			name:   "bugunun basi reset istemez",
			millis: UsageDayStart(now).UnixMilli(),
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

func TestSetUsageLocation(t *testing.T) {
	kullanimKonumu(t, "Europe/Istanbul")
	now := utc(t, "2026-10-03T22:30:00Z")

	// Çözülemeyen ad mevcut konumu bozmamalı.
	for _, gecersiz := range []string{"", "  ", "Mars/Olympus"} {
		if err := SetUsageLocation(gecersiz); err == nil {
			t.Errorf("SetUsageLocation(%q) hata dönmedi", gecersiz)
		}
		if got := UsageLocation().String(); got != "Europe/Istanbul" {
			t.Fatalf("geçersiz ad %q konumu değiştirdi: %s", gecersiz, got)
		}
	}

	if err := SetUsageLocation("UTC"); err != nil {
		t.Fatalf("SetUsageLocation(UTC) başarısız: %v", err)
	}
	if got, want := UsageDayStart(now), utc(t, "2026-10-03T00:00:00Z"); !got.Equal(want) {
		t.Errorf("UTC konumunda gün başı = %s, beklenen %s", got, want)
	}
}

func TestCountsAsReviewAnswer(t *testing.T) {
	// Kutu 1 öğrenme ekranına, kutu 6 öğrenilmiş kelimeye aittir.
	want := map[int]bool{0: false, 1: false, 2: true, 3: true, 4: true, 5: true, 6: false, 7: false}
	for box, sayilir := range want {
		if got := CountsAsReviewAnswer(box); got != sayilir {
			t.Errorf("CountsAsReviewAnswer(%d) = %v, beklenen %v", box, got, sayilir)
		}
	}
}

func TestUnlockedGroupKey(t *testing.T) {
	cases := []struct {
		name   string
		level  string
		group  int
		want   string
		wantOK bool
	}{
		{name: "gecerli anahtar", level: "A1", group: 3, want: "A1:3", wantOK: true},
		{name: "son seviye ve ust sinir", level: "C2", group: MaxUnlockGroupNumber, want: "C2:500", wantOK: true},
		{name: "bilinmeyen seviye", level: "X9", group: 3},
		{name: "kucuk harfli seviye", level: "a1", group: 3},
		{name: "bos seviye", level: "", group: 3},
		{name: "grup sifir", level: "A1", group: 0},
		{name: "negatif grup", level: "A1", group: -1},
		{name: "ust sinirin ustu", level: "A1", group: MaxUnlockGroupNumber + 1},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := UnlockedGroupKey(c.level, c.group)
			if got != c.want || ok != c.wantOK {
				t.Errorf("UnlockedGroupKey(%q, %d) = (%q, %v), beklenen (%q, %v)",
					c.level, c.group, got, ok, c.want, c.wantOK)
			}
		})
	}
}

func TestReviewAdPassesForCheckpoint(t *testing.T) {
	cases := []struct {
		name       string
		checkpoint int
		want       int
		wantOK     bool
	}{
		{name: "ilk kapi", checkpoint: 30, want: 1, wantOK: true},
		{name: "ikinci kapi", checkpoint: 60, want: 2, wantOK: true},
		{name: "son kapi", checkpoint: 90, want: 3, wantOK: true},
		{name: "sifir", checkpoint: 0},
		{name: "negatif", checkpoint: -30},
		{name: "kapi olmayan deger", checkpoint: 45},
		{name: "son kapidan buyuk", checkpoint: 120},
		{name: "kapi sirasi (esik degil)", checkpoint: 2},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ReviewAdPassesForCheckpoint(c.checkpoint)
			if got != c.want || ok != c.wantOK {
				t.Errorf("ReviewAdPassesForCheckpoint(%d) = (%d, %v), beklenen (%d, %v)",
					c.checkpoint, got, ok, c.want, c.wantOK)
			}
		})
	}
}

func TestSplitUnlockedGroups(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{name: "tek anahtar", raw: "A1:3", want: []string{"A1:3"}},
		{name: "birden fazla anahtar sirayi korur", raw: "A1:3,B2:14,A1:4", want: []string{"A1:3", "B2:14", "A1:4"}},
		{name: "bos parcalar atlanir", raw: ",A1:3,,", want: []string{"A1:3"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SplitUnlockedGroups(c.raw); !reflect.DeepEqual(got, c.want) {
				t.Errorf("SplitUnlockedGroups(%q) = %v, beklenen %v", c.raw, got, c.want)
			}
		})
	}

	// Boş küme JSON'da null değil [] olmalı; bu yüzden nil dönmemeli.
	if got := SplitUnlockedGroups(""); got == nil || len(got) != 0 {
		t.Errorf("SplitUnlockedGroups(\"\") = %#v, beklenen boş (nil olmayan) dilim", got)
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
