package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"kelimeler-backend/internal/models"
	"kelimeler-backend/internal/observability"

	"gorm.io/gorm"
)

// Refresh token temizliğinin zamanlaması. İptal edilmiş ve süresi dolmuş
// satırlar 7 gün saklanır: yeniden kullanım tespiti ve destek incelemesi için
// yeterli, tablonun her yenilemede büyümesini engelleyecek kadar kısa.
const (
	refreshTokenCleanupInterval  = time.Hour
	refreshTokenCleanupRetention = 7 * 24 * time.Hour
	refreshTokenCleanupTimeout   = time.Minute
)

// Uygulanmış işlem kayıtlarının (applied_ops) temizliği. Kayıtlar
// models.AppliedOpRetention (30 gün) saklanır; saatte bir bakmak, silinecek
// satırları küçük parçalarda tutar.
const (
	appliedOpsCleanupInterval = time.Hour
	appliedOpsCleanupTimeout  = time.Minute
)

// cleanupFunc, bir temizlik turudur; silinen satır sayısını döner.
type cleanupFunc func(ctx context.Context) (int64, error)

// refreshTokenCleanup, models.DeleteStaleRefreshTokens'ı verilen veritabanına
// bağlar.
func refreshTokenCleanup(database *gorm.DB, retention time.Duration) cleanupFunc {
	return func(ctx context.Context) (int64, error) {
		return models.DeleteStaleRefreshTokens(ctx, database, retention)
	}
}

// appliedOpsCleanup, models.DeleteOldAppliedOps'u verilen veritabanına bağlar.
func appliedOpsCleanup(database *gorm.DB, retention time.Duration) cleanupFunc {
	return func(ctx context.Context) (int64, error) {
		return models.DeleteOldAppliedOps(ctx, database, retention)
	}
}

// startPeriodicCleanup, clean'i hemen bir kez, sonra her interval'de bir
// arka planda çalıştırır. Her tur en fazla timeout sürer.
//
// Dönen stop, döngüyü durdurur ve süren turun bitmesini bekler: kapanmada
// veritabanı havuzu kapatılmadan önce çağrılmalıdır, yoksa yarıda kalan tur
// kapalı havuza sorgu atar ve goroutine sızar. stop birden çok kez çağrılabilir.
// ctx iptal edilince de döngü durur; süren tur ctx'ten türediği için o da kesilir.
//
// Arka plan hatası hiçbir isteğe bağlı değildir, ErrorHandler'a ulaşmaz: bu
// yüzden burada loglanır ve Sentry'ye gönderilir. Kapanmadan kaynaklanan
// iptal raporlanmaz.
func startPeriodicCleanup(ctx context.Context, name string, interval, timeout time.Duration, clean cleanupFunc) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			runCleanup(ctx, name, timeout, clean)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			wg.Wait()
		})
	}
}

// runCleanup, tek bir temizlik turunu timeout sınırıyla çalıştırır.
func runCleanup(ctx context.Context, name string, timeout time.Duration, clean cleanupFunc) {
	if ctx.Err() != nil {
		return
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	n, err := clean(runCtx)
	if err != nil {
		if ctx.Err() != nil {
			// Kapanma sırasında kesildi: hata değil.
			return
		}
		log.Printf("%s başarısız: %v", name, err)
		observability.CaptureError(fmt.Errorf("%s: %w", name, err))
		return
	}
	if n > 0 {
		log.Printf("%s: %d satır silindi", name, n)
	}
}
