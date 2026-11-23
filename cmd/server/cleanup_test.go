package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"kelimeler-backend/internal/db"
	"kelimeler-backend/internal/db/dbtest"
	"kelimeler-backend/internal/models"

	"github.com/getsentry/sentry-go"
)

// sayacTemizlik, her çağrıyı sayan ve çağrıyı kanala bildiren sahte bir
// temizlik turudur.
type sayacTemizlik struct {
	cagri    atomic.Int64
	cagrildi chan context.Context
	fn       func(ctx context.Context) (int64, error)
}

func yeniSayacTemizlik(fn func(ctx context.Context) (int64, error)) *sayacTemizlik {
	return &sayacTemizlik{cagrildi: make(chan context.Context, 100), fn: fn}
}

func (s *sayacTemizlik) temizle(ctx context.Context) (int64, error) {
	s.cagri.Add(1)
	s.cagrildi <- ctx
	if s.fn != nil {
		return s.fn(ctx)
	}
	return 0, nil
}

func (s *sayacTemizlik) bekle(t *testing.T) context.Context {
	t.Helper()
	select {
	case ctx := <-s.cagrildi:
		return ctx
	case <-time.After(5 * time.Second):
		t.Fatal("temizlik turu çalışmadı")
		return nil
	}
}

// durdurBekle, stop'un makul sürede döndüğünü doğrular: dönmüyorsa goroutine
// sızıyor ya da süren tur kesilmiyor demektir.
func durdurBekle(t *testing.T, stop func()) {
	t.Helper()
	bitti := make(chan struct{})
	go func() { stop(); close(bitti) }()
	select {
	case <-bitti:
	case <-time.After(5 * time.Second):
		t.Fatal("stop dönmedi: temizlik goroutine'i durmuyor")
	}
}

func TestPeriodicCleanup_HemenVeSonraAraliklaCalisir(t *testing.T) {
	s := yeniSayacTemizlik(nil)
	stop := startPeriodicCleanup(context.Background(), "test temizliği", 20*time.Millisecond, time.Minute, s.temizle)
	defer durdurBekle(t, stop)

	// İlk tur açılışta, aralık beklenmeden çalışır.
	ctx := s.bekle(t)
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("tur context'inde zaman aşımı yok")
	}
	if kalan := time.Until(deadline); kalan > time.Minute || kalan <= 0 {
		t.Errorf("tur zaman aşımı = %s, beklenen (0, 1dk]", kalan)
	}
	// Sonraki turlar aralıkla gelir.
	s.bekle(t)
	s.bekle(t)
}

func TestPeriodicCleanup_StopSurenTuruKeserVeBekler(t *testing.T) {
	var turBitti atomic.Bool
	s := yeniSayacTemizlik(func(ctx context.Context) (int64, error) {
		<-ctx.Done() // asılı sorgu: yalnızca iptalle biter
		time.Sleep(20 * time.Millisecond)
		turBitti.Store(true)
		return 0, ctx.Err()
	})
	stop := startPeriodicCleanup(context.Background(), "test temizliği", time.Hour, time.Hour, s.temizle)
	s.bekle(t)

	durdurBekle(t, stop)
	if !turBitti.Load() {
		t.Error("stop süren turun bitmesini beklemeden döndü (veritabanı havuzu tur sürerken kapanırdı)")
	}
	// İkinci çağrı güvenlidir ve beklemez.
	durdurBekle(t, stop)

	n := s.cagri.Load()
	time.Sleep(50 * time.Millisecond)
	if got := s.cagri.Load(); got != n {
		t.Errorf("stop'tan sonra %d tur daha çalıştı", got-n)
	}
}

func TestPeriodicCleanup_UstContextIptalindeDurur(t *testing.T) {
	s := yeniSayacTemizlik(nil)
	ctx, cancel := context.WithCancel(context.Background())
	stop := startPeriodicCleanup(ctx, "test temizliği", 10*time.Millisecond, time.Minute, s.temizle)
	s.bekle(t)

	cancel() // SIGTERM karşılığı
	durdurBekle(t, stop)
	n := s.cagri.Load()
	time.Sleep(50 * time.Millisecond)
	if got := s.cagri.Load(); got != n {
		t.Errorf("sinyalden sonra %d tur daha çalıştı", got-n)
	}
}

func TestPeriodicCleanup_HataBildirilirDonguSurer_KapanmaIptaliBildirilmez(t *testing.T) {
	transport := &kayitTransport{}
	if err := sentry.Init(sentry.ClientOptions{Dsn: "https://anahtar@sentry.invalid/1", Transport: transport}); err != nil {
		t.Fatalf("sentry başlatılamadı: %v", err)
	}
	t.Cleanup(func() { sentry.CurrentHub().BindClient(nil) })

	var mu sync.Mutex
	hataVer := true
	s := yeniSayacTemizlik(func(ctx context.Context) (int64, error) {
		mu.Lock()
		defer mu.Unlock()
		if hataVer {
			hataVer = false
			return 0, errors.New("bağlantı koptu")
		}
		<-ctx.Done() // ikinci tur kapanmaya kadar asılı kalır
		return 0, ctx.Err()
	})
	stop := startPeriodicCleanup(context.Background(), "test temizliği", 10*time.Millisecond, time.Hour, s.temizle)
	s.bekle(t) // hata veren tur
	s.bekle(t) // hatadan sonra döngü sürer
	durdurBekle(t, stop)

	olaylar := transport.olaylar()
	if len(olaylar) != 1 {
		t.Fatalf("Sentry'ye %d olay gitti, beklenen 1 (yalnızca gerçek hata; kapanma iptali değil)", len(olaylar))
	}
	if ex := olaylar[0].Exception; len(ex) == 0 || !strings.Contains(ex[len(ex)-1].Value, "test temizliği") {
		t.Errorf("Sentry olayı temizliğin adını taşımıyor: %+v", olaylar[0].Exception)
	}
}

// Gerçek veritabanında: main'in kurduğu temizlik, saklama süresini aşan
// satırları siler, güncel oturuma dokunmaz.
func TestRefreshTokenCleanup_EskiSatirlariSilerGuncelOturumaDokunmaz(t *testing.T) {
	gdb, err := db.Connect(dbtest.DSN(t))
	if err != nil {
		t.Fatalf("veritabanına bağlanılamadı: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := gdb.DB(); err == nil {
			sqlDB.Close()
		}
	})
	if err := db.Migrate(gdb); err != nil {
		t.Fatalf("migrate başarısız: %v", err)
	}

	user := &models.User{
		Email:       "cleanup-" + time.Now().Format("20060102150405.000000000") + "@example.test",
		DisplayName: "Temizlik",
	}
	if err := gdb.Create(user).Error; err != nil {
		t.Fatalf("kullanıcı oluşturulamadı: %v", err)
	}
	t.Cleanup(func() { gdb.Delete(&models.User{}, "id = ?", user.ID) })

	now := time.Now()
	eskiIptal := now.Add(-8 * 24 * time.Hour)
	yakinIptal := now.Add(-2 * 24 * time.Hour)
	satirlar := map[string]*models.RefreshToken{
		"süresi 8 gün önce dolmuş": {UserID: user.ID, TokenHash: "cleanup-eski-" + user.ID, ExpiresAt: now.Add(-8 * 24 * time.Hour)},
		"8 gün önce iptal edilmiş": {UserID: user.ID, TokenHash: "cleanup-iptal-" + user.ID, ExpiresAt: now.Add(time.Hour), RevokedAt: &eskiIptal},
		"2 gün önce iptal edilmiş": {UserID: user.ID, TokenHash: "cleanup-yakin-" + user.ID, ExpiresAt: now.Add(time.Hour), RevokedAt: &yakinIptal},
		"etkin":                    {UserID: user.ID, TokenHash: "cleanup-etkin-" + user.ID, ExpiresAt: now.Add(24 * time.Hour)},
	}
	for ad, rt := range satirlar {
		if err := gdb.Create(rt).Error; err != nil {
			t.Fatalf("%s satırı yazılamadı: %v", ad, err)
		}
	}

	// Turlar kısa aralıkla tekrarlanır: aynı veritabanını kullanan başka bir
	// paket testi advisory lock'u o an tutuyorsa tur (0, nil) ile atlar,
	// sonraki tur siler. Eski satırlar gidene kadar beklenir; stop ancak
	// ondan sonra çağrılır, yoksa süren silme kapanma iptaliyle kesilirdi.
	hatalar := make(chan error, 1000)
	gercek := refreshTokenCleanup(gdb, refreshTokenCleanupRetention)
	stop := startPeriodicCleanup(t.Context(), "refresh token temizliği", 20*time.Millisecond, refreshTokenCleanupTimeout,
		func(ctx context.Context) (int64, error) {
			n, err := gercek(ctx)
			if err != nil && ctx.Err() == nil {
				hatalar <- err
			}
			return n, err
		})
	eskiIDler := []uint64{satirlar["süresi 8 gün önce dolmuş"].ID, satirlar["8 gün önce iptal edilmiş"].ID}
	sinir := time.Now().Add(10 * time.Second)
	for {
		var kalan int64
		if err := gdb.Model(&models.RefreshToken{}).Where("id IN ?", eskiIDler).Count(&kalan).Error; err != nil {
			t.Fatalf("sayım başarısız: %v", err)
		}
		if kalan == 0 {
			break
		}
		select {
		case err := <-hatalar:
			t.Fatalf("temizlik: %v", err)
		default:
		}
		if time.Now().After(sinir) {
			t.Fatalf("eski satırlar 10 sn içinde silinmedi (kalan %d)", kalan)
		}
		time.Sleep(10 * time.Millisecond)
	}
	durdurBekle(t, stop)

	beklenen := map[string]bool{
		"süresi 8 gün önce dolmuş": false,
		"8 gün önce iptal edilmiş": false,
		"2 gün önce iptal edilmiş": true,
		"etkin":                    true,
	}
	for ad, kalmali := range beklenen {
		var n int64
		if err := gdb.Model(&models.RefreshToken{}).Where("id = ?", satirlar[ad].ID).Count(&n).Error; err != nil {
			t.Fatalf("sayım başarısız: %v", err)
		}
		if (n == 1) != kalmali {
			t.Errorf("%s: kaldı = %v, beklenen %v", ad, n == 1, kalmali)
		}
	}
}

// Gerçek veritabanında: uygulanmış işlem kayıtlarının temizliği 30 günden
// eski satırları siler, yenilerine dokunmaz.
func TestAppliedOpsCleanup_EskiKayitlariSiler(t *testing.T) {
	gdb, err := db.Connect(dbtest.DSN(t))
	if err != nil {
		t.Fatalf("veritabanına bağlanılamadı: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := gdb.DB(); err == nil {
			sqlDB.Close()
		}
	})
	if err := db.Migrate(gdb); err != nil {
		t.Fatalf("migrate başarısız: %v", err)
	}

	user := &models.User{
		Email:       "ops-cleanup-" + time.Now().Format("20060102150405.000000000") + "@example.test",
		DisplayName: "Temizlik",
	}
	if err := gdb.Create(user).Error; err != nil {
		t.Fatalf("kullanıcı oluşturulamadı: %v", err)
	}
	t.Cleanup(func() { gdb.Delete(&models.User{}, "id = ?", user.ID) })

	now := time.Now()
	satirlar := map[string]*models.AppliedOp{
		"31 gün önce": {UserID: user.ID, OpID: "00000000-0000-4000-8000-000000000031", Kind: "swipe", AppliedAt: now.Add(-31 * 24 * time.Hour), Result: "{}"},
		"29 gün önce": {UserID: user.ID, OpID: "00000000-0000-4000-8000-000000000029", Kind: "swipe", AppliedAt: now.Add(-29 * 24 * time.Hour), Result: "{}"},
		"bugün":       {UserID: user.ID, OpID: "00000000-0000-4000-8000-000000000000", Kind: "learn", AppliedAt: now, Result: "{}"},
	}
	for ad, op := range satirlar {
		if err := gdb.Create(op).Error; err != nil {
			t.Fatalf("%s satırı yazılamadı: %v", ad, err)
		}
	}

	// Aynı veritabanını kullanan başka bir paket advisory lock'u tutuyorsa tur
	// atlanır; eski satır gidene kadar yeniden denenir.
	temizle := appliedOpsCleanup(gdb, models.AppliedOpRetention)
	sinir := time.Now().Add(10 * time.Second)
	for {
		if _, err := temizle(t.Context()); err != nil {
			t.Fatalf("temizlik: %v", err)
		}
		var kalan int64
		if err := gdb.Model(&models.AppliedOp{}).
			Where("user_id = ? AND op_id = ?", user.ID, satirlar["31 gün önce"].OpID).Count(&kalan).Error; err != nil {
			t.Fatalf("sayım başarısız: %v", err)
		}
		if kalan == 0 {
			break
		}
		if time.Now().After(sinir) {
			t.Fatal("eski kayıt 10 sn içinde silinmedi")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var kalan int64
	if err := gdb.Model(&models.AppliedOp{}).Where("user_id = ?", user.ID).Count(&kalan).Error; err != nil {
		t.Fatalf("sayım başarısız: %v", err)
	}
	if kalan != 2 {
		t.Errorf("kalan kayıt = %d, beklenen 2 (29 gün önce ve bugün)", kalan)
	}
}
