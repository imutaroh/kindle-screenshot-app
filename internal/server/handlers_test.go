package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/imutaroh/kindle-screenshot-app/internal/session"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	mgr := session.NewManager(t.TempDir())
	return New(mgr, t.TempDir())
}

func TestHandleIndex_ReturnsFormDefaults(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	body := rec.Body.String()

	if !strings.Contains(body, `/static/vendor/htmx.min.js`) {
		t.Error("body does not include htmx script tag")
	}
	if !strings.Contains(body, `value="50"`) {
		t.Error("body does not include pdf_pages_per_file default (50)")
	}
	if !strings.Contains(body, `value="0"`) {
		t.Error("body does not include max_pages default (0)")
	}
	if !strings.Contains(body, `value="left" checked`) {
		t.Error("body does not include direction=left as checked")
	}
	if !strings.Contains(body, `id="autoDeletePng" name="auto_delete_png" checked`) {
		t.Error("body does not include auto_delete_png checked by default")
	}
}

func TestHandleUIStart_EmptyBookName_Returns422WithError(t *testing.T) {
	srv := newTestServer(t)

	form := url.Values{"book_name": {"   "}}
	req := httptest.NewRequest(http.MethodPost, "/ui/start", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "本の名前を入力してください") {
		t.Error("body does not include validation error message")
	}
	if !strings.Contains(body, `class="input-error"`) {
		t.Error("body does not include input-error class on the book name field")
	}
	if !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Error("body does not include an oob swap for the book name field")
	}

	// キャプチャは開始されていないはず。
	if srv.mgr.IsRunning() {
		t.Error("manager should not be running after a validation error")
	}
}

func TestHandleUIStatus_NotRunning_HasNoPollingTrigger(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/ui/status", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "hx-trigger") {
		t.Errorf("non-running fragment should not carry a polling hx-trigger, got: %s", body)
	}
	if !strings.Contains(body, `id="statusArea"`) {
		t.Error("body does not include #statusArea root")
	}
}

func TestHandleUIStatus_Running_HasPollingTrigger(t *testing.T) {
	srv := newTestServer(t)
	srv.mgr.SetRunningForTest(true)

	req := httptest.NewRequest(http.MethodGet, "/ui/status", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `hx-get="/ui/status" hx-trigger="every 1s" hx-swap="outerHTML"`) {
		t.Errorf("running fragment should carry the self-polling trigger, got: %s", body)
	}
	if !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Error("running fragment should include the oob status badge")
	}
}

func TestHandleReaderAndBooksAndPDFs_Removed(t *testing.T) {
	srv := newTestServer(t)

	for _, path := range []string{"/reader", "/api/books", "/pdfs/a/b.pdf"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404 (removed reader feature)", path, rec.Code)
		}
	}
}

func TestOldJSONAPIRemoved(t *testing.T) {
	srv := newTestServer(t)

	for _, path := range []string{"/api/start", "/api/stop", "/api/status", "/api/config"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404 (removed API)", path, rec.Code)
		}
	}
}
