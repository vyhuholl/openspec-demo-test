package acceptance

import (
	"net/http"
	"testing"
)

// Базовое поведение одиночных броней. Касание: на стадии 1 можно, на стадии 2 — 409 (B2).
func TestBase_SingleBooking(t *testing.T) {
	touching := http.StatusCreated
	if stage >= 2 {
		touching = http.StatusConflict
	}
	tests := []struct {
		name       string
		start, end string
		wantStatus int
	}{
		{"free slot", "2027-11-09T13:00:00Z", "2027-11-09T14:00:00Z", http.StatusCreated},
		{"overlap", "2027-11-09T10:30:00Z", "2027-11-09T11:30:00Z", http.StatusConflict},
		{"touching end of existing", "2027-11-09T11:00:00Z", "2027-11-09T12:00:00Z", touching},
		{"touching start of existing", "2027-11-09T09:00:00Z", "2027-11-09T10:00:00Z", touching},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := room(t)
			seed := mustCreateSingle(t, r, "2027-11-09T10:00:00Z", "2027-11-09T11:00:00Z")
			resp := createSingle(t, r, tt.start, tt.end)
			wantStatus(t, resp, tt.wantStatus)
			if tt.wantStatus != http.StatusCreated {
				wantNothingCreated(t, r, "2027-11-08", "2027-11-10", seed)
			}
		})
	}
}

// B1: зазор не меньше 10 минут в обе стороны; ровно 10 — можно. Другие комнаты не влияют (B4).
func TestBuffer_Single(t *testing.T) {
	requireStage(t, 2)
	tests := []struct {
		name       string
		otherRoom  bool
		start, end string
		wantStatus int
	}{
		{"after, exactly 10 min", false, "2027-11-09T11:10:00Z", "2027-11-09T12:00:00Z", http.StatusCreated},
		{"after, 9 min", false, "2027-11-09T11:09:00Z", "2027-11-09T12:00:00Z", http.StatusConflict},
		{"before, exactly 10 min", false, "2027-11-09T09:00:00Z", "2027-11-09T09:50:00Z", http.StatusCreated},
		{"before, 9 min", false, "2027-11-09T09:00:00Z", "2027-11-09T09:51:00Z", http.StatusConflict},
		{"touching in another room", true, "2027-11-09T11:00:00Z", "2027-11-09T12:00:00Z", http.StatusCreated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := room(t)
			seed := mustCreateSingle(t, r, "2027-11-09T10:00:00Z", "2027-11-09T11:00:00Z")
			target := r
			if tt.otherRoom {
				target = r + "_other"
			}
			wantStatus(t, createSingle(t, target, tt.start, tt.end), tt.wantStatus)
			if tt.wantStatus != http.StatusCreated {
				wantNothingCreated(t, r, "2027-11-08", "2027-11-10", seed)
			}
		})
	}
}

// B3 + ответ на ловушку-противоречие из промпта change 2: вхождение, нарушающее буфер,
// отклоняет всю серию (409), как и пересечение. Пропускать вхождение нельзя.
func TestBuffer_Series_ViolationRejectsWholeSeries(t *testing.T) {
	requireStage(t, 2)
	r := room(t)
	seed := mustCreateSingle(t, r, "2027-11-03T08:00:00Z", "2027-11-03T08:55:00Z")

	resp := createSeries(t, daily(r, "2027-11-01T09:00:00Z", "2027-11-01T10:00:00Z", "2027-11-05"))
	wantStatus(t, resp, http.StatusConflict)
	wantNothingCreated(t, r, "2027-10-31", "2027-11-06", seed)
}

// B1 + B3: ровно 10 минут до вхождения серии — серия создаётся целиком.
func TestBuffer_Series_ExactlyTenMinutes(t *testing.T) {
	requireStage(t, 2)
	r := room(t)
	mustCreateSingle(t, r, "2027-11-03T08:00:00Z", "2027-11-03T08:50:00Z")

	s := mustCreateSeries(t, daily(r, "2027-11-01T09:00:00Z", "2027-11-01T10:00:00Z", "2027-11-05"))
	wantStarts(t, s.Bookings,
		"2027-11-01T09:00:00Z", "2027-11-02T09:00:00Z", "2027-11-03T09:00:00Z", "2027-11-04T09:00:00Z")
	checkNoOverlap(t, listRange(t, r, "2027-10-31", "2027-11-06"))
}

// B3: буфер вокруг вхождений серии действует и на последующие одиночные брони.
func TestBuffer_SingleNextToSeriesOccurrence(t *testing.T) {
	requireStage(t, 2)
	tests := []struct {
		name       string
		start, end string
		wantStatus int
	}{
		{"5 min after occurrence", "2027-11-02T10:05:00Z", "2027-11-02T11:00:00Z", http.StatusConflict},
		{"10 min after occurrence", "2027-11-02T10:10:00Z", "2027-11-02T11:00:00Z", http.StatusCreated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := room(t)
			mustCreateSeries(t, daily(r, "2027-11-01T09:00:00Z", "2027-11-01T10:00:00Z", "2027-11-03"))
			wantStatus(t, createSingle(t, r, tt.start, tt.end), tt.wantStatus)
		})
	}
}