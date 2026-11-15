package services

import "testing"

func TestIndexOfLevel(t *testing.T) {
	if got := IndexOfLevel("A1"); got != 0 {
		t.Errorf("IndexOfLevel(A1) = %d, beklenen 0", got)
	}
	if got := IndexOfLevel("C2"); got != 5 {
		t.Errorf("IndexOfLevel(C2) = %d, beklenen 5", got)
	}
	if got := IndexOfLevel("D1"); got != -1 {
		t.Errorf("IndexOfLevel(D1) = %d, bilinmeyen seviye -1 dönmeli", got)
	}
}

func TestAdvanceGroup(t *testing.T) {
	cases := []struct {
		name        string
		level       string
		group       int
		maxGroup    int
		wantLevel   string
		wantGroup   int
		wantChanged bool
	}{
		{
			name:  "seviye içinde bir sonraki gruba geçer",
			level: "A1", group: 3, maxGroup: 12,
			wantLevel: "A1", wantGroup: 4, wantChanged: false,
		},
		{
			name:  "seviyenin son grubu bitince sonraki seviyenin 1. grubuna geçer",
			level: "A1", group: 12, maxGroup: 12,
			wantLevel: "A2", wantGroup: 1, wantChanged: true,
		},
		{
			name:  "B2 bitince C1'e geçer",
			level: "B2", group: 28, maxGroup: 28,
			wantLevel: "C1", wantGroup: 1, wantChanged: true,
		},
		{
			// C2 son seviyedir: bitince group=0 ile "hepsi tamamlandı" işaretlenir.
			name:  "C2 son grubu bitince group 0 doner",
			level: "C2", group: 20, maxGroup: 20,
			wantLevel: "C2", wantGroup: 0, wantChanged: false,
		},
		{
			// Tümü tamamlandıysa ikinci çağrı C2 Grup 1'e geri sarmaz.
			name:  "C2 tamamlanmisken degismez",
			level: "C2", group: 0, maxGroup: 20,
			wantLevel: "C2", wantGroup: 0, wantChanged: false,
		},
		{
			name:  "taninmayan seviyede degismez",
			level: "a1", group: 3, maxGroup: 0,
			wantLevel: "a1", wantGroup: 3, wantChanged: false,
		},
		{
			// Eski doğrulamasız yazımdan kalan grup 0, seviyenin başıdır.
			name:  "son seviye disinda grup 0 ilk gruba gecer",
			level: "A1", group: 0, maxGroup: 12,
			wantLevel: "A1", wantGroup: 1, wantChanged: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lvl, grp, changed := AdvanceGroup(c.level, c.group, c.maxGroup)
			if lvl != c.wantLevel || grp != c.wantGroup || changed != c.wantChanged {
				t.Errorf("AdvanceGroup(%s, %d, %d) = (%s, %d, %v), beklenen (%s, %d, %v)",
					c.level, c.group, c.maxGroup, lvl, grp, changed,
					c.wantLevel, c.wantGroup, c.wantChanged)
			}
		})
	}
}

func TestCompareGroupPositions(t *testing.T) {
	cases := []struct {
		aLevel string
		aGroup int
		bLevel string
		bGroup int
		want   int
	}{
		{"A1", 2, "A1", 3, -1},
		{"A1", 3, "A1", 3, 0},
		{"A2", 1, "A1", 30, 1},
		{"B1", 5, "C1", 1, -1},
		// C2:0 "tüm seviyeler bitti": her konumun ötesinde.
		{"C2", 0, "C2", 40, 1},
		{"C2", 40, "C2", 0, -1},
		{"C2", 0, "C2", 0, 0},
		// Son seviye dışında grup 0, seviyenin başıdır.
		{"A1", 0, "A1", 1, -1},
		// Tanınmayan seviye her konumun gerisinde.
		{"XYZ", 9, "A1", 1, -1},
	}
	for _, c := range cases {
		if got := CompareGroupPositions(c.aLevel, c.aGroup, c.bLevel, c.bGroup); got != c.want {
			t.Errorf("CompareGroupPositions(%s:%d, %s:%d) = %d, beklenen %d",
				c.aLevel, c.aGroup, c.bLevel, c.bGroup, got, c.want)
		}
	}
}

func TestGroupLockReason(t *testing.T) {
	cases := []struct {
		name                                 string
		previousLevel, started, previousSeen bool
		open                                 int
		want                                 string
	}{
		{"acik", true, false, true, 0, ""},
		{"onceki seviye bitmedi", false, false, true, 0, LockReasonPreviousLevel},
		// Önceki seviye, başlanmış grubu da kilitler (eski davranış).
		{"onceki seviye bitmedi baslanmis grup", false, true, true, 0, LockReasonPreviousLevel},
		{"onceki grup gorulmedi", true, false, false, 0, LockReasonPreviousNotSeen},
		// Birden çok neden: önceki grup sınırdan önce gelir.
		{"onceki grup gorulmedi ve sinir dolu", true, false, false, MaxOpenGroups, LockReasonPreviousNotSeen},
		{"sinirin bir alti", true, false, true, MaxOpenGroups - 1, ""},
		{"sinir dolu", true, false, true, MaxOpenGroups, LockReasonOpenGroupLimit},
		// Sınır açık bir grubu kapatmaz.
		{"sinir dolu baslanmis grup", true, true, false, MaxOpenGroups + 1, ""},
	}
	for _, c := range cases {
		if got := GroupLockReason(c.previousLevel, c.started, c.previousSeen, c.open); got != c.want {
			t.Errorf("%s: GroupLockReason = %q, beklenen %q", c.name, got, c.want)
		}
	}
}
