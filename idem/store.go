package idem

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	StatusInFlight  = "in_flight"
	StatusCompleted = "completed"
	StatusFailed    = "failed"

	BeginOutcomeStarted   = "started"
	BeginOutcomeInFlight  = "in_flight"
	BeginOutcomeCompleted = "completed"
	BeginOutcomeFailed    = "failed"

	DefaultTTLSeconds = 86400
	MaxTTLSeconds     = 7 * 86400
	MaxBodyBytes      = 200000
)

var (
	keyRe         = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)
	scopeRe       = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)
	fingerprintRe = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

var ErrNotFound = errors.New("record not found")

type ValidationError struct {
	Code    string `json:"code"`
	Message string `json:"error"`
}

func (e ValidationError) Error() string { return e.Message }

type ConflictError struct {
	Code    string `json:"code"`
	Message string `json:"error"`
}

func (e ConflictError) Error() string { return e.Message }

type GoneError struct {
	Code    string `json:"code"`
	Message string `json:"error"`
}

func (e GoneError) Error() string { return e.Message }

type Record struct {
	Key                string          `json:"key"`
	Scope              string          `json:"scope"`
	RequestFingerprint string          `json:"request_fingerprint"`
	Status             string          `json:"status"`
	ResponseStatus     int             `json:"response_status,omitempty"`
	ResponseBody       json.RawMessage `json:"response_body,omitempty"`
	AttemptCount       int             `json:"attempt_count"`
	CreatedAt          time.Time       `json:"created_at"`
	CompletedAt        *time.Time      `json:"completed_at,omitempty"`
	ExpiresAt          time.Time       `json:"expires_at"`
}

type BeginInput struct {
	Key                string `json:"key"`
	Scope              string `json:"scope"`
	RequestFingerprint string `json:"request_fingerprint"`
	TTLSeconds         int    `json:"ttl_seconds,omitempty"`
}

type CompleteInput struct {
	Scope          string          `json:"scope"`
	ResponseStatus int             `json:"response_status"`
	ResponseBody   json.RawMessage `json:"response_body,omitempty"`
}

type Metrics struct {
	Total        int            `json:"total"`
	ByStatus     map[string]int `json:"by_status"`
	ByScope      map[string]int `json:"by_scope"`
	ExpiredCount int            `json:"expired_count"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

type Store struct {
	mu      sync.RWMutex
	records map[string]Record
	nowFn   func() time.Time
}

func NewStore() *Store {
	return &Store{records: map[string]Record{}, nowFn: func() time.Time { return time.Now().UTC() }}
}

func (s *Store) SetNowFn(f func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nowFn = f
}

func (s *Store) now() time.Time { return s.nowFn().UTC() }

func compositeKey(scope, key string) string { return scope + "|" + key }

func (s *Store) BeginRecord(in BeginInput) (Record, string, error) {
	if err := validateKey(in.Key); err != nil {
		return Record{}, "", err
	}
	if err := validateScope(in.Scope); err != nil {
		return Record{}, "", err
	}
	if err := validateFingerprint(in.RequestFingerprint); err != nil {
		return Record{}, "", err
	}
	ttl, err := resolveTTL(in.TTLSeconds)
	if err != nil {
		return Record{}, "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	ck := compositeKey(in.Scope, in.Key)
	existing, ok := s.records[ck]
	if ok {
		if !now.Before(existing.ExpiresAt) {
			rec := buildRecord(in, now, ttl)
			s.records[ck] = rec
			return rec, BeginOutcomeStarted, nil
		}
		if existing.RequestFingerprint != in.RequestFingerprint {
			return Record{}, "", ConflictError{"fingerprint_mismatch", fmt.Sprintf("key %s reused with different fingerprint", in.Key)}
		}
		existing.AttemptCount++
		s.records[ck] = existing
		return existing, statusToOutcome(existing.Status), nil
	}
	rec := buildRecord(in, now, ttl)
	s.records[ck] = rec
	return rec, BeginOutcomeStarted, nil
}

func buildRecord(in BeginInput, now time.Time, ttl time.Duration) Record {
	return Record{
		Key:                in.Key,
		Scope:              in.Scope,
		RequestFingerprint: in.RequestFingerprint,
		Status:             StatusInFlight,
		AttemptCount:       1,
		CreatedAt:          now,
		ExpiresAt:          now.Add(ttl),
	}
}

func statusToOutcome(status string) string {
	switch status {
	case StatusInFlight:
		return BeginOutcomeInFlight
	case StatusCompleted:
		return BeginOutcomeCompleted
	case StatusFailed:
		return BeginOutcomeFailed
	}
	return status
}

func (s *Store) CompleteRecord(key, scope string, in CompleteInput) (Record, error) {
	return s.finishRecord(key, scope, in, StatusCompleted)
}

func (s *Store) FailRecord(key, scope string, in CompleteInput) (Record, error) {
	return s.finishRecord(key, scope, in, StatusFailed)
}

func (s *Store) finishRecord(key, scope string, in CompleteInput, finalStatus string) (Record, error) {
	if err := validateKey(key); err != nil {
		return Record{}, err
	}
	if scope == "" && in.Scope != "" {
		scope = in.Scope
	}
	if scope == "" {
		return Record{}, ValidationError{"invalid_scope", "scope is required (query ?scope= or body.scope)"}
	}
	if err := validateScope(scope); err != nil {
		return Record{}, err
	}
	if in.Scope != "" && in.Scope != scope {
		return Record{}, ValidationError{"scope_mismatch", "body.scope must match query ?scope="}
	}
	if in.ResponseStatus < 100 || in.ResponseStatus > 599 {
		return Record{}, ValidationError{"invalid_response_status", "response_status must be 100..599"}
	}
	if len(in.ResponseBody) > 0 && !json.Valid(in.ResponseBody) {
		return Record{}, ValidationError{"invalid_response_body", "response_body must be valid JSON"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	ck := compositeKey(scope, key)
	rec, ok := s.records[ck]
	if !ok {
		return Record{}, ErrNotFound
	}
	if !now.Before(rec.ExpiresAt) {
		return Record{}, GoneError{"key_expired", fmt.Sprintf("key %s expired", key)}
	}
	if rec.Status != StatusInFlight {
		return Record{}, ConflictError{"already_finished", fmt.Sprintf("record already %s", rec.Status)}
	}
	rec.Status = finalStatus
	rec.ResponseStatus = in.ResponseStatus
	rec.ResponseBody = in.ResponseBody
	rec.CompletedAt = &now
	s.records[ck] = rec
	return rec, nil
}

func (s *Store) GetRecord(key, scope string) (Record, error) {
	if err := validateKey(key); err != nil {
		return Record{}, err
	}
	if err := validateScope(scope); err != nil {
		return Record{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.records[compositeKey(scope, key)]
	if !ok {
		return Record{}, ErrNotFound
	}
	return rec, nil
}

func (s *Store) ListRecords(status, scope string) ([]Record, error) {
	if status != "" && status != StatusInFlight && status != StatusCompleted && status != StatusFailed {
		return nil, ValidationError{"invalid_status", "status must be in_flight, completed or failed"}
	}
	if scope != "" {
		if err := validateScope(scope); err != nil {
			return nil, err
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []Record{}
	for _, rec := range s.records {
		if status != "" && rec.Status != status {
			continue
		}
		if scope != "" && rec.Scope != scope {
			continue
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].Key < out[j].Key
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func (s *Store) DeleteRecord(key, scope string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	if err := validateScope(scope); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ck := compositeKey(scope, key)
	if _, ok := s.records[ck]; !ok {
		return ErrNotFound
	}
	delete(s.records, ck)
	return nil
}

func (s *Store) Metrics() Metrics {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := s.now()
	m := Metrics{
		ByStatus:  map[string]int{StatusInFlight: 0, StatusCompleted: 0, StatusFailed: 0},
		ByScope:   map[string]int{},
		UpdatedAt: now,
	}
	for _, rec := range s.records {
		m.Total++
		m.ByStatus[rec.Status]++
		m.ByScope[rec.Scope]++
		if !now.Before(rec.ExpiresAt) {
			m.ExpiredCount++
		}
	}
	return m
}

// EvictExpired removes records whose ExpiresAt <= now. Returns number removed.
func (s *Store) EvictExpired() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	count := 0
	for ck, rec := range s.records {
		if !now.Before(rec.ExpiresAt) {
			delete(s.records, ck)
			count++
		}
	}
	return count
}

func validateKey(key string) error {
	if !keyRe.MatchString(key) {
		return ValidationError{"invalid_key", "key must match ^[A-Za-z0-9_-]{8,128}$"}
	}
	return nil
}

func validateScope(scope string) error {
	if !scopeRe.MatchString(scope) {
		return ValidationError{"invalid_scope", "scope must match ^[a-z][a-z0-9._-]{0,63}$"}
	}
	return nil
}

func validateFingerprint(fp string) error {
	if !fingerprintRe.MatchString(fp) {
		return ValidationError{"invalid_fingerprint", "request_fingerprint must be 64 lowercase hex chars (sha256)"}
	}
	return nil
}

func resolveTTL(ttl int) (time.Duration, error) {
	if ttl == 0 {
		return time.Duration(DefaultTTLSeconds) * time.Second, nil
	}
	if ttl < 1 || ttl > MaxTTLSeconds {
		return 0, ValidationError{"invalid_ttl_seconds", fmt.Sprintf("ttl_seconds must be 1..%d", MaxTTLSeconds)}
	}
	return time.Duration(ttl) * time.Second, nil
}

func DefaultStore() *Store {
	s := NewStore()
	now := s.now()
	completedAt := now.Add(-30 * time.Second)
	s.records[compositeKey("payments.create", "idem_completed_demo")] = Record{
		Key: "idem_completed_demo", Scope: "payments.create",
		RequestFingerprint: strings.Repeat("a", 64),
		Status:             StatusCompleted, ResponseStatus: 201,
		ResponseBody: json.RawMessage(`{"order_id":"ord_demo_001","amount_cents":1999}`),
		AttemptCount: 1,
		CreatedAt:    now.Add(-2 * time.Minute), CompletedAt: &completedAt,
		ExpiresAt: now.Add(time.Duration(DefaultTTLSeconds) * time.Second),
	}
	s.records[compositeKey("orders.refund", "idem_inflight_demo")] = Record{
		Key: "idem_inflight_demo", Scope: "orders.refund",
		RequestFingerprint: strings.Repeat("b", 64),
		Status:             StatusInFlight, AttemptCount: 1,
		CreatedAt: now.Add(-10 * time.Second),
		ExpiresAt: now.Add(time.Duration(DefaultTTLSeconds) * time.Second),
	}
	failedAt := now.Add(-1 * time.Minute)
	s.records[compositeKey("payments.create", "idem_failed_demo")] = Record{
		Key: "idem_failed_demo", Scope: "payments.create",
		RequestFingerprint: strings.Repeat("c", 64),
		Status:             StatusFailed, ResponseStatus: 422,
		ResponseBody: json.RawMessage(`{"error":"insufficient_funds"}`),
		AttemptCount: 1,
		CreatedAt:    now.Add(-3 * time.Minute), CompletedAt: &failedAt,
		ExpiresAt: now.Add(time.Duration(DefaultTTLSeconds) * time.Second),
	}
	s.records[compositeKey("payments.create", "idem_expired_demo")] = Record{
		Key: "idem_expired_demo", Scope: "payments.create",
		RequestFingerprint: strings.Repeat("d", 64),
		Status:             StatusInFlight, AttemptCount: 1,
		CreatedAt: now.Add(-7 * 24 * time.Hour),
		ExpiresAt: now.Add(-1 * time.Hour),
	}
	return s
}
