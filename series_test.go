package acceptance

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// Коды вопросов (Q1-Q7) — в комментариях к тестам.

func daily(room, start, end, until string) seriesReq {
	return seriesReq{"room": room, "start": start, "end": end, "repeat": "daily", "until": until}
}

func weekly(room, start, end, until string, days ...string) seriesReq {
	r := seriesReq{"room": room, "start": start, "end": end, "repeat": "weekly", "until": until}
	if days != nil {
		r["days"] = days
	}
	return r
}

func wantNothingCreated(t *testing.T, room, from, to string, keep ...booking) {
	t.Helper()
	got := listRange(t, room, from, to)
	if !slices.Equal(ids(got), ids(keep)) {
		t.Fatalf("room %s has bookings %v, want only pre-existing %v", room, starts(got), starts(keep))
	}
}

// Контракт (LOW): 201, брони по возрастанию start, время в UTC, видны в GET /bookings.
func TestSeries_Daily_CreatedAndListed(t *testing.T) {
	r := room(t)
	s := mustCreateSeries(t, daily(r, "2027-11-01T09:00:00Z", "2027-11-01T10:00:00Z", "2027-11-05"))

	wantStarts(t, s.Bookings,
		"2027-11-01T09:00:00Z", "2027-11-02T09:00:00Z", "2027-11-03T09:00:00Z", "2027-11-04T09:00:00Z")
	if n := len(s.Bookings); n != 4 && n != 5 {
		t.Fatalf("got %d bookings, want 4 or 5 (inclusivity of until is checked separately)", n)
	}
	for i, b := range s.Bookings {
		if b.Room != r {
			t.Fatalf("bookings[%d].room = %q, want %q", i, b.Room, r)
		}
		if b.ID == "" {
			t.Fatalf("bookings[%d].id is empty", i)
		}
		if d := instant(t, b.End).Sub(instant(t, b.Start)); d != time.Hour {
			t.Fatalf("bookings[%d] duration = %v, want 1h", i, d)
		}
		if !strings.HasSuffix(b.Start, "Z") || !strings.HasSuffix(b.End, "Z") {
			t.Fatalf("bookings[%d] = %s–%s, want RFC3339 in UTC", i, b.Start, b.End)
		}
		if i > 0 && !instant(t, s.Bookings[i-1].Start).Before(instant(t, b.Start)) {
			t.Fatalf("bookings not sorted by start: %v", starts(s.Bookings))
		}
	}

	listed := listRange(t, r, "2027-10-31", "2027-11-07")
	if !slices.Equal(ids(listed), ids(s.Bookings)) {
		t.Fatalf("GET /bookings shows %v, want exactly the series bookings %v", starts(listed), starts(s.Bookings))
	}
}

// Q1: повторение по местному времени Europe/Berlin, после перевода часов UTC сдвигается.
func TestSeries_DST(t *testing.T) {
	tests := []struct {
		name       string
		req        func(room string) seriesReq
		wantStarts []string
	}{
		{
			name: "autumn weekly keeps 10:00 Berlin",
			req: func(r string) seriesReq {
				return weekly(r, "2027-10-25T08:00:00Z", "2027-10-25T09:00:00Z", "2027-11-09")
			},
			wantStarts: []string{"2027-10-25T08:00:00Z", "2027-11-01T09:00:00Z", "2027-11-08T09:00:00Z"},
		},
		{
			name: "spring daily keeps 10:00 Berlin",
			req: func(r string) seriesReq {
				return daily(r, "2028-03-24T09:00:00Z", "2028-03-24T10:00:00Z", "2028-03-28")
			},
			wantStarts: []string{
				"2028-03-24T09:00:00Z", "2028-03-25T09:00:00Z", "2028-03-26T08:00:00Z", "2028-03-27T08:00:00Z",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := mustCreateSeries(t, tt.req(room(t)))
			wantStarts(t, s.Bookings, tt.wantStarts...)
			for i := range tt.wantStarts {
				b := s.Bookings[i]
				if d := instant(t, b.End).Sub(instant(t, b.Start)); d != time.Hour {
					t.Fatalf("bookings[%d] = %s–%s, want 1h", i, b.Start, b.End)
				}
			}
		})
	}
}

// Q1, уточнение: местное (берлинское) время начала или конца в [02:00, 03:00) — 400,
// даже если серия не попадает на дату перевода часов. Ровно 03:00 — можно.
func TestSeries_DSTTransitionHour(t *testing.T) {
	tests := []struct {
		name       string
		req        func(room string) seriesReq
		wantStatus int
		wantStarts []string
	}{
		{
			name: "start in spring gap",
			req: func(r string) seriesReq {
				return daily(r, "2028-03-24T01:30:00Z", "2028-03-24T02:30:00Z", "2028-03-27")
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "start in autumn repeated hour",
			req: func(r string) seriesReq {
				return daily(r, "2027-10-29T00:30:00Z", "2027-10-29T01:30:00Z", "2027-11-01")
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "end in transition hour",
			req: func(r string) seriesReq {
				return daily(r, "2028-03-24T00:30:00Z", "2028-03-24T01:30:00Z", "2028-03-27")
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "transition hour without transition date",
			req: func(r string) seriesReq {
				return weekly(r, "2027-11-01T01:30:00Z", "2027-11-01T02:30:00Z", "2027-11-15")
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "03:00 across spring change",
			req: func(r string) seriesReq {
				return daily(r, "2028-03-24T02:00:00Z", "2028-03-24T03:00:00Z", "2028-03-28")
			},
			wantStatus: http.StatusCreated,
			wantStarts: []string{
				"2028-03-24T02:00:00Z", "2028-03-25T02:00:00Z", "2028-03-26T01:00:00Z", "2028-03-27T01:00:00Z",
			},
		},
		{
			name: "03:00 across autumn change",
			req: func(r string) seriesReq {
				return daily(r, "2027-10-29T01:00:00Z", "2027-10-29T02:00:00Z", "2027-11-02")
			},
			wantStatus: http.StatusCreated,
			wantStarts: []string{
				"2027-10-29T01:00:00Z", "2027-10-30T01:00:00Z", "2027-10-31T02:00:00Z", "2027-11-01T02:00:00Z",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := room(t)
			resp := createSeries(t, tt.req(r))
			wantStatus(t, resp, tt.wantStatus)
			if tt.wantStatus != http.StatusCreated {
				wantNothingCreated(t, r, "2027-10-28", "2027-11-16")
				wantNothingCreated(t, r, "2028-03-23", "2028-03-28")
				return
			}
			wantStarts(t, decodeSeries(t, resp).Bookings, tt.wantStarts...)
		})
	}
}

// Q2: until включительно; серия из одного вхождения допустима; until раньше start — 400.
func TestSeries_Until(t *testing.T) {
	tests := []struct {
		name       string
		until      string
		wantStatus int
		wantStarts []string
	}{
		{
			name:       "last day included",
			until:      "2027-11-03",
			wantStatus: http.StatusCreated,
			wantStarts: []string{"2027-11-01T09:00:00Z", "2027-11-02T09:00:00Z", "2027-11-03T09:00:00Z"},
		},
		{
			name:       "until equals start date",
			until:      "2027-11-01",
			wantStatus: http.StatusCreated,
			wantStarts: []string{"2027-11-01T09:00:00Z"},
		},
		{
			name:       "until before start date",
			until:      "2027-10-31",
			wantStatus: http.StatusBadRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := room(t)
			resp := createSeries(t, daily(r, "2027-11-01T09:00:00Z", "2027-11-01T10:00:00Z", tt.until))
			wantStatus(t, resp, tt.wantStatus)
			if tt.wantStatus != http.StatusCreated {
				wantNothingCreated(t, r, "2027-10-30", "2027-11-03")
				return
			}
			s := decodeSeries(t, resp)
			if len(s.Bookings) != len(tt.wantStarts) {
				t.Fatalf("got %d bookings %v, want %d", len(s.Bookings), starts(s.Bookings), len(tt.wantStarts))
			}
			wantStarts(t, s.Bookings, tt.wantStarts...)
		})
	}
}

// Q3: горизонт серии — не дальше года от даты первого вхождения.
func TestSeries_Horizon(t *testing.T) {
	tests := []struct {
		name       string
		req        func(room string) seriesReq
		wantStatus int
	}{
		{
			name: "exactly one year",
			req: func(r string) seriesReq {
				return weekly(r, "2027-11-01T09:00:00Z", "2027-11-01T10:00:00Z", "2028-11-01")
			},
			wantStatus: http.StatusCreated,
		},
		{
			name: "one year and one day",
			req: func(r string) seriesReq {
				return weekly(r, "2027-11-01T09:00:00Z", "2027-11-01T10:00:00Z", "2028-11-02")
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "hundred years daily",
			req: func(r string) seriesReq {
				return daily(r, "2027-11-01T09:00:00Z", "2027-11-01T10:00:00Z", "2127-11-01")
			},
			wantStatus: http.StatusBadRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := room(t)
			resp := createSeries(t, tt.req(r))
			wantStatus(t, resp, tt.wantStatus)
			if tt.wantStatus != http.StatusCreated {
				wantNothingCreated(t, r, "2027-10-31", "2027-11-15")
			}
		})
	}
}

// Q4: конфликт хотя бы одного вхождения — 409 на всю серию, ни одной брони не создано.
func TestSeries_Conflict_WholeSeriesRejected(t *testing.T) {
	tests := []struct {
		name       string
		seedStart  string
		seedEnd    string
		seedInRoom bool
		wantStatus int
	}{
		{
			name:       "overlap on middle occurrence",
			seedStart:  "2027-11-03T09:30:00Z",
			seedEnd:    "2027-11-03T10:30:00Z",
			seedInRoom: true,
			wantStatus: http.StatusConflict,
		},
		{
			name:       "occurrence nested in existing",
			seedStart:  "2027-11-02T08:00:00Z",
			seedEnd:    "2027-11-02T12:00:00Z",
			seedInRoom: true,
			wantStatus: http.StatusConflict,
		},
		{
			name:       "same time in another room",
			seedStart:  "2027-11-03T09:00:00Z",
			seedEnd:    "2027-11-03T10:00:00Z",
			seedInRoom: false,
			wantStatus: http.StatusCreated,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := room(t)
			seedRoom := r
			if !tt.seedInRoom {
				seedRoom = r + "_other"
			}
			seed := mustCreateSingle(t, seedRoom, tt.seedStart, tt.seedEnd)

			resp := createSeries(t, daily(r, "2027-11-01T09:00:00Z", "2027-11-01T10:00:00Z", "2027-11-05"))
			wantStatus(t, resp, tt.wantStatus)
			if tt.wantStatus == http.StatusCreated {
				return
			}
			wantNothingCreated(t, r, "2027-10-31", "2027-11-06", seed)
		})
	}
}

// Q4: касание (конец одной брони = начало другой) — не конфликт.
func TestSeries_TouchingExisting(t *testing.T) {
	r := room(t)
	before := mustCreateSingle(t, r, "2027-11-03T08:00:00Z", "2027-11-03T09:00:00Z")
	after := mustCreateSingle(t, r, "2027-11-03T10:00:00Z", "2027-11-03T11:00:00Z")

	resp := createSeries(t, daily(r, "2027-11-01T09:00:00Z", "2027-11-01T10:00:00Z", "2027-11-05"))
	wantStatus(t, resp, http.StatusCreated)
	s := decodeSeries(t, resp)
	listed := listRange(t, r, "2027-10-31", "2027-11-06")
	want := append(slices.Clone(s.Bookings), before, after)
	if !slices.Equal(ids(listed), ids(want)) {
		t.Fatalf("room has %v, want series plus both neighbours", starts(listed))
	}
}

// Q5: weekly по выбранным дням.
func TestSeries_Weekly(t *testing.T) {
	tests := []struct {
		name       string
		days       []string
		until      string
		wantStatus int
		wantStarts []string
	}{
		{
			name:       "selected days",
			days:       []string{"mon", "wed", "fri"},
			until:      "2027-11-13",
			wantStatus: http.StatusCreated,
			wantStarts: []string{
				"2027-11-01T09:00:00Z", "2027-11-03T09:00:00Z", "2027-11-05T09:00:00Z",
				"2027-11-08T09:00:00Z", "2027-11-10T09:00:00Z", "2027-11-12T09:00:00Z",
			},
		},
		{
			name:       "days omitted means weekday of start",
			until:      "2027-11-16",
			wantStatus: http.StatusCreated,
			wantStarts: []string{"2027-11-01T09:00:00Z", "2027-11-08T09:00:00Z", "2027-11-15T09:00:00Z"},
		},
		{
			name:       "start weekday not in days",
			days:       []string{"tue", "thu"},
			until:      "2027-11-13",
			wantStatus: http.StatusBadRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := room(t)
			resp := createSeries(t, weekly(r, "2027-11-01T09:00:00Z", "2027-11-01T10:00:00Z", tt.until, tt.days...))
			wantStatus(t, resp, tt.wantStatus)
			if tt.wantStatus != http.StatusCreated {
				wantNothingCreated(t, r, "2027-10-31", "2027-11-14")
				return
			}
			s := decodeSeries(t, resp)
			if len(s.Bookings) != len(tt.wantStarts) {
				t.Fatalf("got %d bookings %v, want %d", len(s.Bookings), starts(s.Bookings), len(tt.wantStarts))
			}
			wantStarts(t, s.Bookings, tt.wantStarts...)
		})
	}
}

// Q5 + соглашения сервиса: невалидный запрос — 400, ничего не создано.
func TestSeries_InvalidRequest(t *testing.T) {
	const start, end = "2027-11-01T09:00:00Z", "2027-11-01T10:00:00Z"
	tests := []struct {
		name string
		req  func(room string) seriesReq
	}{
		{"unknown repeat", func(r string) seriesReq {
			q := daily(r, start, end, "2027-11-05")
			q["repeat"] = "monthly"
			return q
		}},
		{"missing repeat", func(r string) seriesReq {
			q := daily(r, start, end, "2027-11-05")
			delete(q, "repeat")
			return q
		}},
		{"unknown day", func(r string) seriesReq { return weekly(r, start, end, "2027-11-19", "mon", "funday") }},
		{"days with daily", func(r string) seriesReq {
			q := daily(r, start, end, "2027-11-05")
			q["days"] = []string{"mon"}
			return q
		}},
		{"missing until", func(r string) seriesReq {
			q := daily(r, start, end, "")
			delete(q, "until")
			return q
		}},
		{"until not a date", func(r string) seriesReq { return daily(r, start, end, "06.11.2026") }},
		{"start not in utc", func(r string) seriesReq { return daily(r, "2027-11-01T12:00:00+03:00", end, "2027-11-05") }},
		{"end equals start", func(r string) seriesReq { return daily(r, start, start, "2027-11-05") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := room(t)
			wantStatus(t, createSeries(t, tt.req(r)), http.StatusBadRequest)
			wantNothingCreated(t, r, "2027-10-31", "2027-11-19")
		})
	}
}

// Q6: вхождения одной серии пересекаются между собой — 400, ничего не создано.
// Два подтеста разделяют инвариант (объективно) и выбор кода ответа (решение заказчика).
func TestSeries_SelfOverlap(t *testing.T) {
	r := room(t)
	resp := createSeries(t, daily(r, "2027-11-01T09:00:00Z", "2027-11-02T10:00:00Z", "2027-11-04"))

	t.Run("no double booking", func(t *testing.T) {
		got := listRange(t, r, "2027-10-31", "2027-11-06")
		checkNoOverlap(t, got)
		if len(got) != 0 {
			t.Fatalf("status %d, room has %v, want nothing created", resp.status, starts(got))
		}
	})
	t.Run("status 400", func(t *testing.T) {
		wantStatus(t, resp, http.StatusBadRequest)
	})
}
