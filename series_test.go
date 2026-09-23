package acceptance

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Коды пунктов листа заказчика (O1…O7, B1…B4) — в комментариях к тестам.

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

// O2: until включительно; серия из одного вхождения допустима; until раньше start — 400.
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

// O3: горизонт серии — не дальше года от даты первого вхождения.
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

// O4: конфликт хотя бы одного вхождения — 409 на всю серию, ни одной брони не создано.
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

// O4 + B2: касание на стадии 1 не конфликт, на стадии 2 — конфликт из-за буфера.
func TestSeries_TouchingExisting(t *testing.T) {
	r := room(t)
	before := mustCreateSingle(t, r, "2027-11-03T08:00:00Z", "2027-11-03T09:00:00Z")
	after := mustCreateSingle(t, r, "2027-11-03T10:00:00Z", "2027-11-03T11:00:00Z")

	resp := createSeries(t, daily(r, "2027-11-01T09:00:00Z", "2027-11-01T10:00:00Z", "2027-11-05"))
	if stage >= 2 {
		wantStatus(t, resp, http.StatusConflict)
		wantNothingCreated(t, r, "2027-10-31", "2027-11-06", before, after)
		return
	}
	wantStatus(t, resp, http.StatusCreated)
	s := decodeSeries(t, resp)
	listed := listRange(t, r, "2027-10-31", "2027-11-06")
	want := append(slices.Clone(s.Bookings), before, after)
	if !slices.Equal(ids(listed), ids(want)) {
		t.Fatalf("room has %v, want series plus both neighbours", starts(listed))
	}
}

// O5: weekly по выбранным дням.
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

// O5 + соглашения сервиса: невалидный запрос — 400, ничего не создано.
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
		{"empty days", func(r string) seriesReq {
			q := weekly(r, start, end, "2027-11-19")
			q["days"] = []string{}
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

// O6: вхождения одной серии пересекаются между собой — 400, ничего не создано.
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

// O1: повторение по местному времени Europe/Berlin, после перевода часов UTC сдвигается.
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

// O7: при одновременных запросах брони комнаты не пересекаются, серия либо целиком, либо никак.
func TestSeries_Concurrent_InvariantHolds(t *testing.T) {
	const rounds, seriesPerRound, singlesPerRound = 5, 25, 5
	for round := range rounds {
		t.Run(fmt.Sprintf("round %d", round+1), func(t *testing.T) {
			r := room(t)
			base := time.Date(2027, 11, 1, 9, 0, 0, 0, time.UTC)

			type result struct {
				status int
				ids    []string
			}
			results := make([]result, seriesPerRound+singlesPerRound)
			ready := make(chan struct{})
			var wg sync.WaitGroup
			for i := range seriesPerRound {
				start := base.Add(time.Duration(i) * 5 * time.Minute)
				req := daily(r, start.Format(time.RFC3339), start.Add(time.Hour).Format(time.RFC3339), "2027-11-05")
				wg.Go(func() {
					<-ready
					resp := createSeries(t, req)
					res := result{status: resp.status}
					if resp.status == http.StatusCreated {
						res.ids = ids(decodeSeries(t, resp).Bookings)
					}
					results[i] = res
				})
			}
			for j := range singlesPerRound {
				start := base.AddDate(0, 0, 2).Add(time.Duration(j) * 17 * time.Minute)
				i := seriesPerRound + j
				wg.Go(func() {
					<-ready
					resp := createSingle(t, r, start.Format(time.RFC3339), start.Add(time.Hour).Format(time.RFC3339))
					res := result{status: resp.status}
					if resp.status == http.StatusCreated {
						var b booking
						if err := json.Unmarshal(resp.body, &b); err != nil {
							t.Errorf("decode booking: %v", err)
						}
						res.ids = []string{b.ID}
					}
					results[i] = res
				})
			}
			close(ready)
			wg.Wait()

			var wantIDs []string
			for i, res := range results {
				if res.status != http.StatusCreated && res.status != http.StatusConflict {
					t.Fatalf("request %d: status %d, want 201 or 409", i, res.status)
				}
				wantIDs = append(wantIDs, res.ids...)
			}
			if len(wantIDs) == 0 {
				t.Fatalf("nothing created, want at least one success")
			}
			slices.Sort(wantIDs)

			listed := listRange(t, r, "2027-10-31", "2027-11-06")
			if !slices.Equal(ids(listed), wantIDs) {
				t.Fatalf("room has %d bookings, successful responses report %d: partial series or lost writes",
					len(listed), len(wantIDs))
			}
			checkNoOverlap(t, listed)
		})
	}
}