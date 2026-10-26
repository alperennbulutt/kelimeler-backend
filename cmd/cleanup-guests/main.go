// Command cleanup-guests, terk edilmiş misafir hesaplarını siler.
//
// Son etkinliği --inactive-days (varsayılan 180) günden eski, hâlâ misafir
// olan ve hiçbir giriş kimliği bulunmayan hesaplar adaydır; son etkinliğin
// nasıl ölçüldüğü models.FindInactiveGuests'te anlatılır. Varsayılan kip
// --dry-run'dır: adaylar sayılır ve listelenir, hiçbir şey silinmez. Silmek
// için --apply verilir. Zamanlanmış değildir; elle çalıştırılır (bkz.
// DEPLOYMENT.md, "Terk edilmiş misafir temizliği").
//
//	go run ./cmd/cleanup-guests                     # yalnızca rapor
//	go run ./cmd/cleanup-guests --apply             # sil
//	go run ./cmd/cleanup-guests --inactive-days 365 --apply
//
// Yalnızca DATABASE_URL (ve config'in zorunlu tuttuğu APP_ENV) okunur;
// şemaya dokunulmaz (migrate çalıştırılmaz).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/db"
	"kelimeler-backend/internal/models"

	"gorm.io/gorm"
)

const (
	// defaultInactiveDays, --inactive-days verilmezse kullanılır.
	defaultInactiveDays = 180

	// minInactiveDays, kabul edilen en kısa eşiktir: yazım hatasıyla (ör.
	// --inactive-days 1) yeni açılmış misafirlerin silinmesini önler.
	minInactiveDays = 30

	// listLimit, raporda tek tek yazılan aday sayısıdır.
	listLimit = 20
)

// options, komut satırı seçenekleridir.
type options struct {
	inactiveDays int
	apply        bool
}

// result, bir çalıştırmanın özetidir.
type result struct {
	candidates int
	deleted    int
	skipped    int // aday listesinden sonra etkinleşti ya da kaydedildi
	failed     int
}

func main() {
	opts, err := parseFlags(os.Args[1:], os.Stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		log.Fatalf("cleanup-guests: %v", err)
	}

	cfg := config.Load()
	database, err := db.Connect(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("cleanup-guests: db bağlantısı başarısız: %v", err)
	}
	if sqlDB, err := database.DB(); err == nil {
		defer sqlDB.Close()
	}

	res, err := run(context.Background(), database, opts, time.Now(), os.Stdout)
	if err != nil {
		log.Fatalf("cleanup-guests: %v", err)
	}
	if res.failed > 0 {
		log.Printf("cleanup-guests: %d misafir silinemedi (ayrıntı yukarıda)", res.failed)
		os.Exit(1)
	}
}

// parseFlags, seçenekleri okur ve doğrular.
func parseFlags(args []string, stderr io.Writer) (options, error) {
	fs := flag.NewFlagSet("cleanup-guests", flag.ContinueOnError)
	fs.SetOutput(stderr)
	opts := options{}
	fs.IntVar(&opts.inactiveDays, "inactive-days", defaultInactiveDays,
		"son etkinliği bu kadar günden eski misafirler silinir (en az 30)")
	fs.BoolVar(&opts.apply, "apply", false, "adayları gerçekten sil (verilmezse yalnızca rapor)")
	dryRun := fs.Bool("dry-run", true, "yalnızca rapor (varsayılan); --apply ile birlikte verilemez")
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if fs.NArg() > 0 {
		return options{}, fmt.Errorf("beklenmeyen argüman: %v", fs.Args())
	}
	dryRunSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "dry-run" {
			dryRunSet = true
		}
	})
	if opts.apply && dryRunSet && *dryRun {
		return options{}, errors.New("--apply ve --dry-run birlikte verilemez")
	}
	if opts.inactiveDays < minInactiveDays {
		return options{}, fmt.Errorf("--inactive-days en az %d olmalı (%d verildi)", minInactiveDays, opts.inactiveDays)
	}
	return opts, nil
}

// run, adayları bulur, raporlar ve opts.apply ise siler. now, eşiğin
// hesaplandığı andır (testler sabitler).
func run(ctx context.Context, database *gorm.DB, opts options, now time.Time, out io.Writer) (result, error) {
	cutoff := now.Add(-time.Duration(opts.inactiveDays) * 24 * time.Hour)
	guests, err := models.FindInactiveGuests(ctx, database, cutoff)
	if err != nil {
		return result{}, fmt.Errorf("adaylar okunamadı: %w", err)
	}
	res := result{candidates: len(guests)}
	fmt.Fprintf(out, "misafir temizliği: son etkinliği %s öncesi (%d gün) olan %d misafir\n",
		cutoff.UTC().Format(time.RFC3339), opts.inactiveDays, len(guests))
	for i, g := range guests {
		if i == listLimit {
			fmt.Fprintf(out, "  ... ve %d misafir daha\n", len(guests)-listLimit)
			break
		}
		fmt.Fprintf(out, "  %s  açılış %s  son etkinlik %s\n", g.ID,
			g.CreatedAt.UTC().Format("2006-01-02"), g.LastActivity.UTC().Format("2006-01-02"))
	}
	if !opts.apply {
		fmt.Fprintln(out, "dry-run: hiçbir şey silinmedi (silmek için --apply)")
		return res, nil
	}

	for _, g := range guests {
		deleted, err := models.DeleteInactiveGuest(ctx, database, g.ID, cutoff)
		switch {
		case err != nil:
			res.failed++
			fmt.Fprintf(out, "  HATA %s: %v\n", g.ID, err)
		case deleted:
			res.deleted++
		default:
			res.skipped++
		}
	}
	fmt.Fprintf(out, "silindi: %d, atlandı (bu arada etkinleşti): %d, hata: %d\n", res.deleted, res.skipped, res.failed)
	return res, nil
}
