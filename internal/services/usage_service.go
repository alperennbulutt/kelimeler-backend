package services

import (
	"errors"
	"strconv"
	"strings"
	"time"

	// Çalışma imajında (distroless) saat dilimi veritabanı bulunmayabilir;
	// gömülü kopya, konumun her ortamda aynı şekilde yüklenmesini sağlar.
	_ "time/tzdata"
)

// DefaultUsageTimezone, günlük kullanım sayaçlarının gün sınırını belirleyen
// varsayılan saat dilimidir: kullanıcıların tamamına yakını Türkiye'de olduğu
// için "bugünkü hak" onların gece yarısında yenilenmelidir.
const DefaultUsageTimezone = "Europe/Istanbul"

// usageLocation, kullanım gününün hesaplandığı konumdur.
//
// Bu konumu YALNIZCA günlük kullanım sayaçları kullanır. Seri/giriş kaydı
// (stats_handler.go) ve tekrar listesi (startOfDay) sürecin yerel gününde
// kalır: login_date bir "date" kolonudur ve bağlantı PreferSimpleProtocol ile
// açıldığından, oradaki saat dilimini değiştirmek kayıtlı tarihleri bir gün
// kaydırabilir.
var usageLocation = defaultUsageLocation()

func defaultUsageLocation() *time.Location {
	loc, err := time.LoadLocation(DefaultUsageTimezone)
	if err != nil {
		// Türkiye 2016'dan beri sabit UTC+3 kullanıyor.
		return time.FixedZone("TRT", 3*3600)
	}
	return loc
}

// SetUsageLocation, kullanım gününün hesaplandığı saat dilimini değiştirir
// (ör. "Europe/Istanbul"). Ad çözülemezse mevcut konum korunur ve hata döner.
//
// Eşzamanlı çağrılara karşı korumalı değildir: yalnızca başlangıçta, istekler
// servis edilmeden önce çağrılmalıdır.
func SetUsageLocation(name string) error {
	// LoadLocation boş adı sessizce UTC sayar; yanlış yapılandırma gün sınırını
	// fark edilmeden üç saat kaydırmasın.
	if strings.TrimSpace(name) == "" {
		return errors.New("saat dilimi adı boş")
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return err
	}
	usageLocation = loc
	return nil
}

// UsageLocation, kullanım gününün hesaplandığı saat dilimini döndürür.
func UsageLocation() *time.Location {
	return usageLocation
}

// UsageDayStart, now'ın kullanım konumundaki gününün başlangıcını (00:00) döndürür.
func UsageDayStart(now time.Time) time.Time {
	y, m, d := now.In(usageLocation).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, usageLocation)
}

// NeedsResetAt, lastResetDate (unix millis) now'ın kullanım gününden önceye
// aitse true döner. Hiç sıfırlanmamış kayıt (0) her zaman sıfırlama ister.
func NeedsResetAt(lastResetMillis int64, now time.Time) bool {
	return lastResetMillis < UsageDayStart(now).UnixMilli()
}

// NeedsReset, lastResetDate (unix millis) bugünden önceki bir kullanım gününe
// aitse true döner.
func NeedsReset(lastResetMillis int64) bool {
	return NeedsResetAt(lastResetMillis, time.Now())
}

// MaxNoFillPassesPerDay, reklam hiç yüklenemediğinde (no-fill) bir kullanıcıya
// gün içinde kaç kez reklamsız geçiş verileceğini sınırlar.
//
// Amaç: AdMob'un gerçekten envanter döndürmediği anlarda kullanıcıyı bloke
// etmemek, ama uçak modu / reklam engelleyici ile sınırsız ücretsiz kullanımı
// da mümkün kılmamak. Sunucu tarafında zorlanır; istemciden değiştirilemez.
const MaxNoFillPassesPerDay = 2

// CountsAsReviewAnswer, swipe ÖNCESİ kutuya göre cevabın günlük tekrar
// sayacına yazılıp yazılmayacağını söyler.
//
// Kutu 1'deki swipe öğrenme ekranından gelir, kutu 6 ise öğrenilmiş kelimedir;
// ikisi de tekrar cevabı değildir.
func CountsAsReviewAnswer(boxBeforeSwipe int) bool {
	return boxBeforeSwipe >= 2 && boxBeforeSwipe <= 5
}

// Tekrar seansında her ReviewAdCheckpointInterval cevapta bir reklam kapısı
// çıkar; gün içinde en çok MaxReviewAdCheckpoints kapı vardır (30., 60. ve 90.
// cevap). İstemcideki kapı eşikleriyle aynı olmalıdır.
const (
	ReviewAdCheckpointInterval = 30
	MaxReviewAdCheckpoints     = 3
)

// ReviewAdPassesForCheckpoint, verilen kapı (30, 60, 90) geçildiğinde günlük
// reklam geçişi sayacının en az kaç olması gerektiğini döndürür. Değer bir
// kapıya denk gelmiyorsa ok=false döner.
func ReviewAdPassesForCheckpoint(checkpoint int) (passes int, ok bool) {
	if checkpoint <= 0 || checkpoint%ReviewAdCheckpointInterval != 0 {
		return 0, false
	}
	passes = checkpoint / ReviewAdCheckpointInterval
	if passes > MaxReviewAdCheckpoints {
		return 0, false
	}
	return passes, true
}

// MaxUnlockGroupNumber, "bugün açılan gruplar" kümesine yazılabilecek en büyük
// grup numarasıdır. Sözlükteki en kalabalık seviyenin çok üstündedir; tek amacı
// istemciden gelen keyfi değerlerin kümeyi sınırsız büyütmesini engellemektir.
const MaxUnlockGroupNumber = 500

// UnlockedGroupKey, bir grubun "bugün açılan gruplar" kümesindeki anahtarını
// ("SEVIYE:GRUP", ör. "A1:3") üretir. Seviye tanınmıyorsa ya da grup numarası
// aralık dışındaysa ok=false döner.
func UnlockedGroupKey(level string, group int) (key string, ok bool) {
	if IndexOfLevel(level) < 0 || group < 1 || group > MaxUnlockGroupNumber {
		return "", false
	}
	return level + ":" + strconv.Itoa(group), true
}

// SplitUnlockedGroups, virgülle ayrılmış kümeyi anahtar listesine çevirir.
// Boş küme için nil değil boş dilim döner: JSON'da null yerine [] görünmelidir.
func SplitUnlockedGroups(raw string) []string {
	keys := []string{}
	for _, k := range strings.Split(raw, ",") {
		if k != "" {
			keys = append(keys, k)
		}
	}
	return keys
}
