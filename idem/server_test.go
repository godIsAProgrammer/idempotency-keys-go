package idem

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func mkServer() http.Handler {
	return NewServer(nil).Handler()
}

func do(t *testing.T, h http.Handler, method, target string, body any) (int, map[string]any) {
	t.Helper()
	var r *http.Request
	if body != nil {
		buf, _ := json.Marshal(body)
		r = httptest.NewRequest(method, target, bytes.NewReader(buf))
		r.Header.Set("content-type", "application/json")
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out map[string]any
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &out)
	}
	return w.Code, out
}

func validFPHex(seed string) string {
	if len(seed) >= 64 {
		return seed[:64]
	}
	return seed + strings.Repeat("0", 64-len(seed))
}

func TestHealth(t *testing.T) {
	code, body := do(t, mkServer(), "GET", "/health", nil)
	if code != 200 || body["ok"] != true {
		t.Fatalf("code=%d body=%+v", code, body)
	}
}

func TestPostRecordsCreatesAndDuplicates(t *testing.T) {
	h := mkServer()
	in := map[string]any{
		"key": "idem_router_001", "scope": "payments.create",
		"request_fingerprint": validFPHex("11"),
	}
	code, body := do(t, h, "POST", "/records", in)
	if code != 201 || body["outcome"] != "started" {
		t.Fatalf("first POST code=%d body=%+v", code, body)
	}
	code2, body2 := do(t, h, "POST", "/records", in)
	if code2 != 200 || body2["outcome"] != "in_flight" {
		t.Fatalf("second POST code=%d body=%+v", code2, body2)
	}
}

func TestPostRecordsFingerprintMismatch(t *testing.T) {
	h := mkServer()
	do(t, h, "POST", "/records", map[string]any{
		"key": "idem_router_002", "scope": "payments.create",
		"request_fingerprint": validFPHex("aa"),
	})
	code, body := do(t, h, "POST", "/records", map[string]any{
		"key": "idem_router_002", "scope": "payments.create",
		"request_fingerprint": validFPHex("bb"),
	})
	if code != 409 || body["code"] != "fingerprint_mismatch" {
		t.Fatalf("code=%d body=%+v", code, body)
	}
}

func TestCompleteFlowEndToEnd(t *testing.T) {
	h := mkServer()
	in := map[string]any{
		"key": "idem_router_010", "scope": "payments.create",
		"request_fingerprint": validFPHex("11"),
	}
	do(t, h, "POST", "/records", in)
	code, body := do(t, h, "POST", "/records/idem_router_010/complete?scope=payments.create", map[string]any{
		"response_status": 201,
		"response_body":   map[string]any{"order_id": "ord_42"},
	})
	if code != 200 {
		t.Fatalf("complete code=%d body=%+v", code, body)
	}
	rec := body["record"].(map[string]any)
	if rec["status"] != "completed" {
		t.Fatalf("status=%v", rec["status"])
	}
	code2, body2 := do(t, h, "POST", "/records", in)
	if code2 != 200 || body2["outcome"] != "completed" {
		t.Fatalf("after complete code=%d body=%+v", code2, body2)
	}
}

func TestCompleteAlreadyExpired(t *testing.T) {
	h := mkServer()
	code, body := do(t, h, "POST", "/records/idem_expired_demo/complete?scope=payments.create", map[string]any{
		"response_status": 200,
	})
	if code != 410 || body["code"] != "key_expired" {
		t.Fatalf("code=%d body=%+v", code, body)
	}
}

func TestGetMissingRecord(t *testing.T) {
	h := mkServer()
	code, body := do(t, h, "GET", "/records/idem_router_999?scope=payments.create", nil)
	if code != 404 || body["code"] != "record_not_found" {
		t.Fatalf("code=%d body=%+v", code, body)
	}
}

func TestListRecordsByStatus(t *testing.T) {
	h := mkServer()
	code, body := do(t, h, "GET", "/records?status=completed", nil)
	if code != 200 {
		t.Fatalf("code=%d", code)
	}
	recs := body["records"].([]any)
	if len(recs) != 1 {
		t.Fatalf("len=%d, want 1", len(recs))
	}
}

func TestListRecordsBadStatus(t *testing.T) {
	h := mkServer()
	code, body := do(t, h, "GET", "/records?status=archived", nil)
	if code != 400 || body["code"] != "invalid_status" {
		t.Fatalf("code=%d body=%+v", code, body)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	h := mkServer()
	code, body := do(t, h, "GET", "/metrics", nil)
	if code != 200 {
		t.Fatalf("code=%d", code)
	}
	m := body["metrics"].(map[string]any)
	if m["total"].(float64) != 4 {
		t.Fatalf("total=%v", m["total"])
	}
}

func TestUnknownRoute(t *testing.T) {
	h := mkServer()
	code, body := do(t, h, "GET", "/whatever", nil)
	if code != 404 || body["code"] != "route_not_found" {
		t.Fatalf("code=%d body=%+v", code, body)
	}
}

func TestMethodNotAllowedOnRecords(t *testing.T) {
	h := mkServer()
	code, body := do(t, h, "PUT", "/records", nil)
	if code != 405 || body["code"] != "method_not_allowed" {
		t.Fatalf("code=%d body=%+v", code, body)
	}
}

func TestInvalidJSON(t *testing.T) {
	h := mkServer()
	r := httptest.NewRequest("POST", "/records", bytes.NewReader([]byte("not json")))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("code=%d", w.Code)
	}
}

func TestRouterDeleteRecord(t *testing.T) {
	h := mkServer()
	code, _ := do(t, h, "DELETE", "/records/idem_completed_demo?scope=payments.create", nil)
	if code != 204 {
		t.Fatalf("code=%d", code)
	}
	code2, body := do(t, h, "GET", "/records/idem_completed_demo?scope=payments.create", nil)
	if code2 != 404 || body["code"] != "record_not_found" {
		t.Fatalf("code=%d body=%+v", code2, body)
	}
}
