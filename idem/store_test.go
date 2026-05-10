package idem

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func newFakeStore() *Store {
	s := NewStore()
	t0 := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	s.SetNowFn(func() time.Time { return t0 })
	return s
}

func validFP(seed string) string {
	if len(seed) >= 64 {
		return seed[:64]
	}
	return seed + strings.Repeat("0", 64-len(seed))
}

func TestBeginRecordCreatesInFlight(t *testing.T) {
	s := newFakeStore()
	rec, outcome, err := s.BeginRecord(BeginInput{
		Key: "idem_alpha_001", Scope: "payments.create",
		RequestFingerprint: validFP("ab"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome != BeginOutcomeStarted {
		t.Fatalf("outcome = %q, want started", outcome)
	}
	if rec.Status != StatusInFlight {
		t.Fatalf("status = %q, want in_flight", rec.Status)
	}
	if rec.AttemptCount != 1 {
		t.Fatalf("attempt_count = %d, want 1", rec.AttemptCount)
	}
	if !rec.ExpiresAt.After(rec.CreatedAt) {
		t.Fatalf("expires_at not after created_at")
	}
}

func TestBeginRecordSameFingerprintReturnsInFlight(t *testing.T) {
	s := newFakeStore()
	in := BeginInput{Key: "idem_alpha_002", Scope: "orders.refund", RequestFingerprint: validFP("c")}
	if _, _, err := s.BeginRecord(in); err != nil {
		t.Fatalf("first begin: %v", err)
	}
	rec, outcome, err := s.BeginRecord(in)
	if err != nil {
		t.Fatalf("second begin: %v", err)
	}
	if outcome != BeginOutcomeInFlight {
		t.Fatalf("outcome = %q, want in_flight", outcome)
	}
	if rec.AttemptCount != 2 {
		t.Fatalf("attempt_count = %d, want 2", rec.AttemptCount)
	}
}

func TestBeginRecordFingerprintMismatch(t *testing.T) {
	s := newFakeStore()
	if _, _, err := s.BeginRecord(BeginInput{Key: "idem_alpha_003", Scope: "payments.create", RequestFingerprint: validFP("a")}); err != nil {
		t.Fatalf("first: %v", err)
	}
	_, _, err := s.BeginRecord(BeginInput{Key: "idem_alpha_003", Scope: "payments.create", RequestFingerprint: validFP("b")})
	var ce ConflictError
	if !errors.As(err, &ce) || ce.Code != "fingerprint_mismatch" {
		t.Fatalf("err = %v, want fingerprint_mismatch", err)
	}
}

func TestBeginRecordExpiredOverwrites(t *testing.T) {
	s := newFakeStore()
	t0 := s.now()
	s.records[compositeKey("payments.create", "idem_alpha_004")] = Record{
		Key: "idem_alpha_004", Scope: "payments.create",
		RequestFingerprint: validFP("a"),
		Status:             StatusInFlight, AttemptCount: 1,
		CreatedAt: t0.Add(-2 * time.Hour), ExpiresAt: t0.Add(-1 * time.Hour),
	}
	rec, outcome, err := s.BeginRecord(BeginInput{Key: "idem_alpha_004", Scope: "payments.create", RequestFingerprint: validFP("b")})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if outcome != BeginOutcomeStarted {
		t.Fatalf("outcome = %q, want started after expiry", outcome)
	}
	if rec.RequestFingerprint != validFP("b") {
		t.Fatalf("fingerprint not overwritten")
	}
	if rec.AttemptCount != 1 {
		t.Fatalf("attempt_count = %d, want reset to 1", rec.AttemptCount)
	}
}

func TestBeginRecordValidations(t *testing.T) {
	s := newFakeStore()
	cases := []struct {
		name string
		in   BeginInput
		code string
	}{
		{"short key", BeginInput{Key: "short", Scope: "payments.create", RequestFingerprint: validFP("a")}, "invalid_key"},
		{"bad scope", BeginInput{Key: "idem_alpha_005", Scope: "Payments.Create", RequestFingerprint: validFP("a")}, "invalid_scope"},
		{"bad fp", BeginInput{Key: "idem_alpha_006", Scope: "payments.create", RequestFingerprint: "not-hex"}, "invalid_fingerprint"},
		{"bad ttl", BeginInput{Key: "idem_alpha_007", Scope: "payments.create", RequestFingerprint: validFP("a"), TTLSeconds: MaxTTLSeconds + 1}, "invalid_ttl_seconds"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := s.BeginRecord(c.in)
			var ve ValidationError
			if !errors.As(err, &ve) || ve.Code != c.code {
				t.Fatalf("err = %v, want %s", err, c.code)
			}
		})
	}
}

func TestCompleteRecordFlowAndAlreadyFinished(t *testing.T) {
	s := newFakeStore()
	in := BeginInput{Key: "idem_alpha_010", Scope: "payments.create", RequestFingerprint: validFP("a")}
	if _, _, err := s.BeginRecord(in); err != nil {
		t.Fatalf("begin: %v", err)
	}
	body := json.RawMessage(`{"order_id":"ord_1"}`)
	rec, err := s.CompleteRecord("idem_alpha_010", "payments.create", CompleteInput{ResponseStatus: 201, ResponseBody: body})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if rec.Status != StatusCompleted || rec.ResponseStatus != 201 {
		t.Fatalf("rec = %+v", rec)
	}
	if rec.CompletedAt == nil {
		t.Fatalf("completed_at not set")
	}
	_, err = s.CompleteRecord("idem_alpha_010", "payments.create", CompleteInput{ResponseStatus: 201, ResponseBody: body})
	var ce ConflictError
	if !errors.As(err, &ce) || ce.Code != "already_finished" {
		t.Fatalf("second complete err = %v, want already_finished", err)
	}
}

func TestCompleteRecordExpiredReturnsGone(t *testing.T) {
	s := newFakeStore()
	t0 := s.now()
	s.records[compositeKey("payments.create", "idem_alpha_011")] = Record{
		Key: "idem_alpha_011", Scope: "payments.create",
		RequestFingerprint: validFP("a"),
		Status:             StatusInFlight, AttemptCount: 1,
		CreatedAt: t0.Add(-2 * time.Hour), ExpiresAt: t0.Add(-1 * time.Hour),
	}
	_, err := s.CompleteRecord("idem_alpha_011", "payments.create", CompleteInput{ResponseStatus: 200})
	var ge GoneError
	if !errors.As(err, &ge) || ge.Code != "key_expired" {
		t.Fatalf("err = %v, want key_expired", err)
	}
}

func TestCompleteRecordValidations(t *testing.T) {
	s := newFakeStore()
	in := BeginInput{Key: "idem_alpha_012", Scope: "payments.create", RequestFingerprint: validFP("a")}
	if _, _, err := s.BeginRecord(in); err != nil {
		t.Fatalf("begin: %v", err)
	}
	cases := []struct {
		name           string
		key, scope     string
		body           CompleteInput
		code           string
	}{
		{"missing scope", "idem_alpha_012", "", CompleteInput{ResponseStatus: 200}, "invalid_scope"},
		{"scope mismatch", "idem_alpha_012", "payments.create", CompleteInput{Scope: "orders.refund", ResponseStatus: 200}, "scope_mismatch"},
		{"bad response status", "idem_alpha_012", "payments.create", CompleteInput{ResponseStatus: 99}, "invalid_response_status"},
		{"bad body", "idem_alpha_012", "payments.create", CompleteInput{ResponseStatus: 200, ResponseBody: json.RawMessage(`not json`)}, "invalid_response_body"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := s.CompleteRecord(c.key, c.scope, c.body)
			var ve ValidationError
			if !errors.As(err, &ve) || ve.Code != c.code {
				t.Fatalf("err = %v, want %s", err, c.code)
			}
		})
	}
}

func TestFailRecordSetsFailedStatus(t *testing.T) {
	s := newFakeStore()
	in := BeginInput{Key: "idem_alpha_020", Scope: "orders.refund", RequestFingerprint: validFP("a")}
	if _, _, err := s.BeginRecord(in); err != nil {
		t.Fatalf("begin: %v", err)
	}
	rec, err := s.FailRecord("idem_alpha_020", "orders.refund", CompleteInput{ResponseStatus: 422, ResponseBody: json.RawMessage(`{"error":"x"}`)})
	if err != nil {
		t.Fatalf("fail: %v", err)
	}
	if rec.Status != StatusFailed {
		t.Fatalf("status = %q", rec.Status)
	}
}

func TestListRecordsFiltering(t *testing.T) {
	s := DefaultStore()
	all, err := s.ListRecords("", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("len(all) = %d, want 4 (default fixture)", len(all))
	}
	completed, _ := s.ListRecords(StatusCompleted, "")
	if len(completed) != 1 || completed[0].Key != "idem_completed_demo" {
		t.Fatalf("completed list = %+v", completed)
	}
	scoped, _ := s.ListRecords("", "payments.create")
	if len(scoped) != 3 {
		t.Fatalf("scoped len = %d, want 3", len(scoped))
	}
	if _, err := s.ListRecords("archived", ""); err == nil {
		t.Fatalf("expected invalid_status error")
	}
}

func TestEvictExpiredAndMetrics(t *testing.T) {
	s := DefaultStore()
	m := s.Metrics()
	if m.Total != 4 {
		t.Fatalf("Total = %d, want 4", m.Total)
	}
	if m.ExpiredCount != 1 {
		t.Fatalf("ExpiredCount = %d, want 1", m.ExpiredCount)
	}
	if m.ByStatus[StatusInFlight] != 2 {
		t.Fatalf("by_status.in_flight = %d, want 2", m.ByStatus[StatusInFlight])
	}
	if m.ByStatus[StatusCompleted] != 1 {
		t.Fatalf("by_status.completed = %d, want 1", m.ByStatus[StatusCompleted])
	}
	removed := s.EvictExpired()
	if removed != 1 {
		t.Fatalf("evicted = %d, want 1", removed)
	}
	m2 := s.Metrics()
	if m2.Total != 3 || m2.ExpiredCount != 0 {
		t.Fatalf("after evict m = %+v", m2)
	}
}

func TestDeleteRecord(t *testing.T) {
	s := DefaultStore()
	if err := s.DeleteRecord("idem_completed_demo", "payments.create"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, err := s.GetRecord("idem_completed_demo", "payments.create")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if err := s.DeleteRecord("idem_completed_demo", "payments.create"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete err = %v, want ErrNotFound", err)
	}
}

func TestDefaultStoreFingerprintsLowercaseHex(t *testing.T) {
	s := DefaultStore()
	for k, rec := range s.records {
		if !fingerprintRe.MatchString(rec.RequestFingerprint) {
			t.Fatalf("record %s has bad fingerprint %q", k, rec.RequestFingerprint)
		}
	}
}
