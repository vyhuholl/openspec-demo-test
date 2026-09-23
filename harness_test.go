// Приёмочные тесты воркшопа. Чёрный ящик: собирают ../cmd/booking, поднимают
// бинарник на свободном порту и ходят в него по HTTP. Внутренние пакеты не импортируются,
// поэтому тесты не зависят от того, как агент устроил код.
//
// Переменные окружения:
//
//	ACCEPTANCE_STAGE=1|2    1 — после change 1 (серии), 2 — после change 2 (буфер). По умолчанию 1.
//	ACCEPTANCE_RACE=0       собрать без -race (по умолчанию с ним; нужен cgo).
//	ACCEPTANCE_BASE_URL=... не собирать и не запускать, бить в уже поднятый сервис.
package acceptance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	baseURL string
	stage   = 1
	client  = &http.Client{Timeout: 30 * time.Second}
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestMain(m *testing.M) {
	if os.Getenv("ACCEPTANCE_STAGE") == "2" {
		stage = 2
	}
	if u := os.Getenv("ACCEPTANCE_BASE_URL"); u != "" {
		baseURL = strings.TrimSuffix(u, "/")
		os.Exit(m.Run())
	}
	os.Exit(runWithServer(m))
}

func runWithServer(m *testing.M) int {
	dir, err := os.MkdirTemp("", "acceptance")
	if err != nil {
		fmt.Fprintln(os.Stderr, "tempdir:", err)
		return 1
	}
	defer os.RemoveAll(dir)

	bin := filepath.Join(dir, "booking")
	args := []string{"build", "-o", bin}
	withRace := os.Getenv("ACCEPTANCE_RACE") != "0"
	if withRace {
		args = append(args, "-race")
	}
	build := exec.Command("go", append(args, "./cmd/booking")...)
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "go build: %v\n%s", err, out)
		return 1
	}

	port, err := freePort()
	if err != nil {
		fmt.Fprintln(os.Stderr, "free port:", err)
		return 1
	}
	var stderr lockedBuffer
	srv := exec.Command(bin)
	srv.Env = append(os.Environ(), "PORT="+port)
	srv.Stdout = io.Discard
	srv.Stderr = &stderr
	if err := srv.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "start service:", err)
		return 1
	}
	defer func() {
		_ = srv.Process.Kill()
		_ = srv.Wait()
	}()

	baseURL = "http://127.0.0.1:" + port
	if err := waitReady(); err != nil {
		fmt.Fprintf(os.Stderr, "service not ready: %v\nstderr:\n%s", err, stderr.String())
		return 1
	}

	code := m.Run()

	if log := stderr.String(); strings.Contains(log, "DATA RACE") {
		fmt.Fprintf(os.Stderr, "\nFAIL: race detector reported a data race in the service\n%s", log)
		return 1
	}
	if withRace {
		fmt.Println("race detector: clean")
	}
	return code
}

func freePort() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer l.Close()
	_, port, err := net.SplitHostPort(l.Addr().String())
	return port, err
}

func waitReady() error {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(baseURL + "/bookings?room=ping&date=2026-01-01")
		if err == nil {
			resp.Body.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("no response from %s", baseURL)
}

// --- HTTP ---

type booking struct {
	ID    string `json:"id"`
	Room  string `json:"room"`
	Start string `json:"start"`
	End   string `json:"end"`
}

type seriesResponse struct {
	ID       string    `json:"id"`
	Bookings []booking `json:"bookings"`
}

type response struct {
	status int
	body   []byte
}

func post(t *testing.T, path string, body any) response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := client.Post(baseURL+path, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return response{status: resp.StatusCode, body: data}
}

// room — уникальная комната на тест: сервис один на весь прогон.
func room(t *testing.T) string {
	return strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
}

func createSingle(t *testing.T, room, start, end string) response {
	t.Helper()
	return post(t, "/bookings", map[string]string{"room": room, "start": start, "end": end})
}

func mustCreateSingle(t *testing.T, room, start, end string) booking {
	t.Helper()
	r := createSingle(t, room, start, end)
	if r.status != http.StatusCreated {
		t.Fatalf("seed booking %s %s–%s: status %d, body %s", room, start, end, r.status, r.body)
	}
	var b booking
	if err := json.Unmarshal(r.body, &b); err != nil {
		t.Fatalf("decode booking %q: %v", r.body, err)
	}
	return b
}

type seriesReq map[string]any

func createSeries(t *testing.T, req seriesReq) response {
	t.Helper()
	return post(t, "/series", req)
}

func decodeSeries(t *testing.T, r response) seriesResponse {
	t.Helper()
	var s seriesResponse
	if err := json.Unmarshal(r.body, &s); err != nil {
		t.Fatalf("decode series %q: %v", r.body, err)
	}
	if s.ID == "" {
		t.Fatalf("series id is empty: %s", r.body)
	}
	return s
}

func mustCreateSeries(t *testing.T, req seriesReq) seriesResponse {
	t.Helper()
	r := createSeries(t, req)
	if r.status != http.StatusCreated {
		t.Fatalf("POST /series %v: status %d, want 201, body %s", req, r.status, r.body)
	}
	return decodeSeries(t, r)
}

func wantStatus(t *testing.T, r response, want int) {
	t.Helper()
	if r.status != want {
		t.Fatalf("status = %d, want %d, body %s", r.status, want, r.body)
	}
	if want >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(r.body, &e); err != nil || e.Error == "" {
			t.Fatalf("error body %q: want {\"error\": \"...\"}", r.body)
		}
	}
}

// listRange собирает брони комнаты за даты [from, to] включительно, без дублей
// (бронь через полночь приходит в выдаче двух дней).
func listRange(t *testing.T, room, from, to string) []booking {
	t.Helper()
	d, err := time.Parse(time.DateOnly, from)
	if err != nil {
		t.Fatalf("from: %v", err)
	}
	last, err := time.Parse(time.DateOnly, to)
	if err != nil {
		t.Fatalf("to: %v", err)
	}
	seen := map[string]bool{}
	var all []booking
	for ; !d.After(last); d = d.AddDate(0, 0, 1) {
		q := url.Values{"room": {room}, "date": {d.Format(time.DateOnly)}}
		resp, err := client.Get(baseURL + "/bookings?" + q.Encode())
		if err != nil {
			t.Fatalf("GET /bookings: %v", err)
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /bookings?%s: status %d, body %s", q.Encode(), resp.StatusCode, data)
		}
		var page struct {
			Bookings []booking `json:"bookings"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			t.Fatalf("decode list %q: %v", data, err)
		}
		for _, b := range page.Bookings {
			if !seen[b.ID] {
				seen[b.ID] = true
				all = append(all, b)
			}
		}
	}
	slices.SortFunc(all, func(a, b booking) int { return strings.Compare(a.ID, b.ID) })
	return all
}

func instant(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return v
}

// wantStarts сверяет моменты начала вхождений (как моменты времени, без учёта
// формы записи смещения) по порядку.
func wantStarts(t *testing.T, got []booking, want ...string) {
	t.Helper()
	if len(got) < len(want) {
		t.Fatalf("got %d bookings, want at least %d: %v", len(got), len(want), starts(got))
	}
	for i, w := range want {
		if !instant(t, got[i].Start).Equal(instant(t, w)) {
			t.Fatalf("bookings[%d].start = %s, want %s; all starts: %v", i, got[i].Start, w, starts(got))
		}
	}
}

func starts(bs []booking) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = b.Start
	}
	return out
}

func ids(bs []booking) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = b.ID
	}
	slices.Sort(out)
	return out
}

// minGap — минимальный допустимый зазор между бронями одной комнаты на текущей стадии.
func minGap() time.Duration {
	if stage >= 2 {
		return 10 * time.Minute
	}
	return 0
}

// checkNoOverlap проверяет инвариант комнаты: брони не пересекаются
// и (на стадии 2) разнесены не меньше чем на буфер.
func checkNoOverlap(t *testing.T, bs []booking) {
	t.Helper()
	sorted := slices.Clone(bs)
	slices.SortFunc(sorted, func(a, b booking) int {
		return instant(t, a.Start).Compare(instant(t, b.Start))
	})
	for i := 1; i < len(sorted); i++ {
		prevEnd := instant(t, sorted[i-1].End)
		curStart := instant(t, sorted[i].Start)
		if curStart.Sub(prevEnd) < minGap() {
			t.Fatalf("bookings %s (%s–%s) and %s (%s–%s) violate room invariant (min gap %v)",
				sorted[i-1].ID, sorted[i-1].Start, sorted[i-1].End,
				sorted[i].ID, sorted[i].Start, sorted[i].End, minGap())
		}
	}
}

func requireStage(t *testing.T, n int) {
	t.Helper()
	if stage < n {
		t.Skipf("stage %d test (ACCEPTANCE_STAGE=%d)", n, stage)
	}
}