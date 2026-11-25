package dto

import (
	"encoding/json"
	"time"

	"kelimeler-backend/internal/models"
)

// --- Progress ---

// OpFields, cevap uçlarının (learn, unknown, swipe) isteğe bağlı çevrimdışı
// mod alanlarıdır. İkisi de verilmezse davranış bu alanlardan önceki gibidir.
//
//   - OpID (UUID): işlemin kimliği. Aynı kimlik ikinci kez gelirse işlem
//     uygulanmaz, ilk yanıt döner (applied_ops).
//   - AnsweredAt (RFC3339): cevabın verildiği an. Sunucu onu
//     [şimdi − 7 gün, şimdi + 5 dk] aralığına kırpar; Leitner tarihleri ve
//     "hangi günün cevabı" kararı bu ana göre verilir.
type OpFields struct {
	OpID       string     `json:"opId"`
	AnsweredAt *time.Time `json:"answeredAt"`
}

type LearnRequest struct {
	WordID  string `json:"wordId"`
	Level   string `json:"level"`
	Group   int    `json:"group"`
	English string `json:"english"`
	Turkish string `json:"turkish"`
	OpFields
}

type SwipeRequest struct {
	WordID    string `json:"wordId"`
	Direction string `json:"direction"` // "right" | "left"
	OpFields
}

// --- Senkronizasyon (çevrimdışı mod) ---

// SyncRequest, POST /progress/sync gövdesidir. İşlemler tek tek çözülür:
// biçimi bozuk bir işlem yalnızca kendisi reddedilir.
type SyncRequest struct {
	Ops []json.RawMessage `json:"ops"`
}

// SyncOp, kuyruktaki tek bir işlemdir; hangi alanların kullanıldığı Kind'a
// bağlıdır (learn/unknown: wordId; swipe: wordId, direction; group_started:
// level, group, usageDate; advance_group, set_current_group: level, group;
// daily_login: date).
type SyncOp struct {
	OpID       string     `json:"opId"`
	Kind       string     `json:"kind"`
	AnsweredAt *time.Time `json:"answeredAt"`
	WordID     string     `json:"wordId"`
	Direction  string     `json:"direction"`
	Level      string     `json:"level"`
	Group      int        `json:"group"`
	UsageDate  string     `json:"usageDate"` // YYYY-MM-DD
	Date       string     `json:"date"`      // YYYY-MM-DD
}

// SyncOpResult, bir işlemin sonucudur. Status: "applied" (uygulandı),
// "noop" (geçerli ama hiçbir şey değiştirmedi, ör. vadesi gelmemiş kelime) ya
// da "rejected" (uygulanmadı; neden Code'da). Code, applied/noop'ta da ek
// bilgi taşıyabilir (ör. free_group_conflict, previous_not_seen); yoksa
// alan gönderilmez. Progress, kelime işlemlerinde kelimenin güncel kaydıdır
// (GET /progress/word/:id biçimi); kelime işlemi değilse ya da kayıt yoksa
// gönderilmez.
type SyncOpResult struct {
	OpID     string          `json:"opId"`
	Status   string          `json:"status"`
	Code     string          `json:"code,omitempty"`
	Progress json.RawMessage `json:"progress,omitempty"`
}

// SyncResponse, POST /progress/sync yanıtıdır. User, GET /auth/me
// yanıtındaki "user" nesnesidir; Usage, GET /users/me/usage yanıtıdır.
type SyncResponse struct {
	Results []SyncOpResult `json:"results"`
	User    *UserDTO       `json:"user"`
	Usage   DailyUsageDTO  `json:"usage"`
}

// UpcomingCounts, UserWordRepository.getUpcomingCountsForBox dönüşünün karşılığı.
// dailyCounts JSON'da string-anahtarlı olur (Flutter int'e parse eder).
type UpcomingCounts struct {
	Today           int            `json:"today"`
	Tomorrow        int            `json:"tomorrow"`
	Later           int            `json:"later"`
	DailyCounts     map[string]int `json:"dailyCounts"`
	TotalMonthCount int            `json:"totalMonthCount"`
	BusiestDayLabel string         `json:"busiestDayLabel"`
	BusiestDayCount int            `json:"busiestDayCount"`
}

// --- Users ---

type UpdateProfileRequest struct {
	DisplayName *string `json:"displayName"`
}

type SetCurrentGroupRequest struct {
	Level       string  `json:"level"`
	Group       int     `json:"group"`
	Title       *string `json:"title"`
	Description *string `json:"description"`
	TotalWords  *int    `json:"totalWords"`
}

// AdvanceGroupRequest, advance-group ucunun isteğe bağlı gövdesidir:
// istemcinin tamamladığı ya da bütün kelimelerini gördüğü grup. Gövde
// verilmezse (eski istemciler) işaretçi yalnızca işaretçideki grup
// tamamlanmışsa bir ileri gider.
type AdvanceGroupRequest struct {
	Level string `json:"level"`
	Group int    `json:"group"`
}

// UnlockGroupRequest, group-started ve group-unlocked-by-ad uçlarının isteğe
// bağlı gövdesidir. Gövde verilmezse uç yalnızca sayacı artırır.
type UnlockGroupRequest struct {
	Level string `json:"level"`
	Group int    `json:"group"`
}

// ReviewAdPassRequest, review-ad-pass ucunun isteğe bağlı gövdesidir: geçilen
// reklam kapısının cevap eşiği (30, 60 ya da 90). Gövde verilmezse uç yalnızca
// sayacı artırır.
type ReviewAdPassRequest struct {
	Checkpoint int `json:"checkpoint"`
}

// DailyUsageDTO alanlarında omitempty yoktur: istemci bir alanın yokluğunu
// "sunucu bu sayacı tutmuyor" diye yorumlar, 0 ve boş dizi de gönderilmelidir.
type DailyUsageDTO struct {
	GroupsStartedTodayCount int `json:"groupsStartedTodayCount"`
	GroupsUnlockedByAdToday int `json:"groupsUnlockedByAdToday"`
	ReviewAdPassCountToday  int `json:"reviewAdPassCountToday"`
	ReviewAnswersToday      int `json:"reviewAnswersToday"`
	// Bugün açılan gruplar, bütün kurslarınki birlikte. İngilizce kursunun
	// anahtarı "SEVIYE:GRUP"tur (ör. "A1:3"; eski biçim), diğer kurslarınki
	// kurs koduyla öneklenir: "KURS/SEVIYE:GRUP" (ör. "de/A1:3"). Bkz.
	// services.CourseUnlockedGroupKey.
	UnlockedGroupsToday   []string `json:"unlockedGroupsToday"`
	NoFillPassesToday     int      `json:"noFillPassesToday"`
	NoFillPassesRemaining int      `json:"noFillPassesRemaining"`
	LastResetDate         int64    `json:"lastResetDate"`
}

type CurrentGroupDTO struct {
	Level       string  `json:"level"`
	Group       int     `json:"group"`
	Title       string  `json:"title"`
	Description *string `json:"description"`
	TotalWords  int     `json:"totalWords"`
}

// --- Map ---

// LevelGroupDTO, haritadaki bir grubun durumudur. isSeen, seenWords ve
// lockReason sonradan eklendi; eski alanların anlamı değişmedi. Yeni
// alanlarda omitempty yoktur: false, 0 ve "" de gönderilir.
type LevelGroupDTO struct {
	GroupNumber     int  `json:"groupNumber"`
	TotalWords      int  `json:"totalWords"`
	RemainingInBox1 int  `json:"remainingInBox1"`
	IsCompleted     bool `json:"isCompleted"`
	IsUnlocked      bool `json:"isUnlocked"`
	HasStarted      bool `json:"hasStarted"`
	// Grubun bütün kelimeleri en az bir kez cevaplandı (bilindi ya da
	// bilinmedi). Tamamlanan grup görülmüştür de.
	IsSeen bool `json:"isSeen"`
	// İlerleme kaydı olan (en az bir kez cevaplanmış) kelime sayısı.
	SeenWords int `json:"seenWords"`
	// Kilitliyse nedeni: "previous_level", "previous_not_seen" ya da
	// "open_group_limit"; açıksa "".
	LockReason string `json:"lockReason"`
}

// OpenGroupDTO, sınıra sayılan açık bir gruptur: başlanmış ama tamamlanmamış,
// önceki seviyesi tamamlanmış (kendisi previous_level ile kilitli değil).
type OpenGroupDTO struct {
	Level           string `json:"level"`
	Group           int    `json:"group"`
	TotalWords      int    `json:"totalWords"`
	RemainingInBox1 int    `json:"remainingInBox1"`
}

// OpenGroupsDTO, GET /map/open-groups yanıtıdır. openGroups seviye ve grup
// sırasındadır; boşsa [] döner.
type OpenGroupsDTO struct {
	OpenGroupCount int            `json:"openGroupCount"`
	MaxOpenGroups  int            `json:"maxOpenGroups"`
	OpenGroups     []OpenGroupDTO `json:"openGroups"`
}

// UnknownWordRequest, POST /progress/unknown gövdesidir: öğrenme ekranında
// kaydı olmayan kelimeye "Bilmiyorum" denendi.
type UnknownWordRequest struct {
	WordID string `json:"wordId"`
	OpFields
}

// SnapshotDTO, GET /sync/snapshot yanıtıdır: çevrimdışı mod istemcisinin
// sakladığı anlık görüntü. Her bölüm, adı geçen ucun yanıtıyla aynı
// biçimdedir; tek alanlı sarmalayıcılar ({"user": …}, {"learnedCount": n},
// {"dates": […]}) açılmıştır.
type SnapshotDTO struct {
	ServerNow         time.Time `json:"serverNow"`
	UsageDate         string    `json:"usageDate"` // YYYY-MM-DD (Europe/Istanbul)
	DictionaryVersion int64     `json:"dictionaryVersion"`
	// User, GET /auth/me yanıtındaki "user" nesnesidir.
	User *UserDTO `json:"user"`
	// Usage, GET /users/me/usage yanıtıdır.
	Usage DailyUsageDTO `json:"usage"`
	// Progress, kutu 1–5'teki bütün kelime kayıtlarıdır (GET
	// /progress/word/:id biçimi), wordId sırasıyla.
	Progress []models.WordProgress `json:"progress"`
	// LearnedCount, GET /home/learned-count'taki learnedCount'tur (kutu 6).
	LearnedCount int64 `json:"learnedCount"`
	// LearnedWordIDs, kutu 6'daki (öğrenilmiş) bütün kelimelerin kimlikleridir,
	// sırayla. İlerlemesi olan her grubun kartları gerektiği için kartları
	// gönderilen (ya da haveGroups ile atlanan) her grubun öğrenilmiş
	// kelimelerini kapsar: istemci kaydı görünmeyen kartın yeni mi öğrenilmiş
	// mi olduğunu buradan bilir. Liste boşsa [] gelir; alanı göndermeyen eski
	// sunucuda istemci haritanın seenWords farkına düşer.
	LearnedWordIDs []string `json:"learnedWordIds"`
	// Levels, her seviye için GET /map/levels/:level/groups yanıtıdır.
	Levels map[string][]LevelGroupDTO `json:"levels"`
	// OpenGroups, GET /map/open-groups yanıtıdır.
	OpenGroups OpenGroupsDTO `json:"openGroups"`
	// Cards, ilerlemesi olan her grubun ve işaretçideki grupla ondan sonraki
	// grubun kartlarıdır (GET /dictionary/words biçimi; seviye, grup ve sıra
	// sırasıyla). İstemcinin dictionaryVersion'ı güncelse haveGroups'ta
	// bildirdiği gruplar atlanır.
	Cards []models.DictionaryWord `json:"cards"`
	// Stats, GET /stats/summary yanıtıdır (newlyEarnedBadges her zaman boş:
	// anlık görüntü rozet bildirimini tüketmez).
	Stats StatsSummaryDTO `json:"stats"`
	// LoginDates, GET /stats/login-dates yanıtındaki "dates" listesidir.
	LoginDates []string `json:"loginDates"`
}
