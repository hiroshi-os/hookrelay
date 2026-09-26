package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("store: not found")

type Endpoint struct {
	ID                   uuid.UUID
	URL                  string
	SecretCurrent        string
	SecretPrevious       string
	Enabled              bool
	ConsecutiveFailures  int
	MaxConcurrency       int
	DisableAfterFailures int
}

type Event struct {
	ID        uuid.UUID
	Type      string
	Payload   json.RawMessage
	CreatedAt time.Time
}

type Delivery struct {
	ID           int64
	EventID      uuid.UUID
	EndpointID   uuid.UUID
	Attempt      int
	Status       string
	NextAttempt  time.Time
	LastError    string
	CreatedAt    time.Time
	EventType    string
	EventPayload json.RawMessage
	EventCreated time.Time
	Endpoint     Endpoint
}

type DeadLetter struct {
	ID         int64
	DeliveryID int64
	EventID    uuid.UUID
	EndpointID uuid.UUID
	Attempts   int
	LastError  string
	Payload    json.RawMessage
	CreatedAt  time.Time
	ReplayedAt *time.Time
}

type Store struct {
	Pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store { return &Store{Pool: pool} }

func (s *Store) CreateEndpoint(ctx context.Context, url, secret string, maxConcurrency, disableAfter int) (Endpoint, error) {
	if maxConcurrency <= 0 {
		maxConcurrency = 8
	}
	if disableAfter <= 0 {
		disableAfter = 20
	}
	var ep Endpoint
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO endpoints (url, secret_current, max_concurrency, disable_after_failures)
		VALUES ($1,$2,$3,$4)
		RETURNING id, url, secret_current, COALESCE(secret_previous,''), enabled,
		          consecutive_failures, max_concurrency, disable_after_failures`,
		url, secret, maxConcurrency, disableAfter,
	).Scan(&ep.ID, &ep.URL, &ep.SecretCurrent, &ep.SecretPrevious, &ep.Enabled,
		&ep.ConsecutiveFailures, &ep.MaxConcurrency, &ep.DisableAfterFailures)
	return ep, err
}

func (s *Store) EnableEndpoint(ctx context.Context, id uuid.UUID) (Endpoint, error) {
	var ep Endpoint
	err := s.Pool.QueryRow(ctx, `
		UPDATE endpoints
		SET enabled = TRUE, consecutive_failures = 0
		WHERE id = $1
		RETURNING id, url, secret_current, COALESCE(secret_previous,''), enabled,
		          consecutive_failures, max_concurrency, disable_after_failures`, id,
	).Scan(&ep.ID, &ep.URL, &ep.SecretCurrent, &ep.SecretPrevious, &ep.Enabled,
		&ep.ConsecutiveFailures, &ep.MaxConcurrency, &ep.DisableAfterFailures)
	if errors.Is(err, pgx.ErrNoRows) {
		return ep, ErrNotFound
	}
	return ep, err
}

func (s *Store) GetEndpoint(ctx context.Context, id uuid.UUID) (Endpoint, error) {
	var ep Endpoint
	err := s.Pool.QueryRow(ctx, `
		SELECT id, url, secret_current, COALESCE(secret_previous,''), enabled,
		       consecutive_failures, max_concurrency, disable_after_failures
		FROM endpoints WHERE id=$1`, id,
	).Scan(&ep.ID, &ep.URL, &ep.SecretCurrent, &ep.SecretPrevious, &ep.Enabled,
		&ep.ConsecutiveFailures, &ep.MaxConcurrency, &ep.DisableAfterFailures)
	if errors.Is(err, pgx.ErrNoRows) {
		return ep, ErrNotFound
	}
	return ep, err
}

func (s *Store) ListEnabledEndpoints(ctx context.Context) ([]Endpoint, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, url, secret_current, COALESCE(secret_previous,''), enabled,
		       consecutive_failures, max_concurrency, disable_after_failures
		FROM endpoints WHERE enabled = TRUE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Endpoint
	for rows.Next() {
		var ep Endpoint
		if err := rows.Scan(&ep.ID, &ep.URL, &ep.SecretCurrent, &ep.SecretPrevious, &ep.Enabled,
			&ep.ConsecutiveFailures, &ep.MaxConcurrency, &ep.DisableAfterFailures); err != nil {
			return nil, err
		}
		out = append(out, ep)
	}
	return out, rows.Err()
}

func (s *Store) InsertEvent(ctx context.Context, typ string, payload json.RawMessage) (Event, error) {
	var ev Event
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO events (type, payload) VALUES ($1,$2)
		RETURNING id, type, payload, created_at`, typ, payload,
	).Scan(&ev.ID, &ev.Type, &ev.Payload, &ev.CreatedAt)
	return ev, err
}

func (s *Store) InsertEventID(ctx context.Context, id uuid.UUID, typ string, payload json.RawMessage) (Event, error) {
	var ev Event
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO events (id, type, payload) VALUES ($1,$2,$3)
		RETURNING id, type, payload, created_at`, id, typ, payload,
	).Scan(&ev.ID, &ev.Type, &ev.Payload, &ev.CreatedAt)
	return ev, err
}

// EnsureDeliveries creates a pending delivery for every enabled endpoint for the event.
func (s *Store) EnsureDeliveries(ctx context.Context, eventID uuid.UUID) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO deliveries (event_id, endpoint_id, status, next_attempt_at)
		SELECT $1, e.id, 'pending', now()
		FROM endpoints e
		WHERE e.enabled = TRUE
		ON CONFLICT (event_id, endpoint_id) DO NOTHING`, eventID)
	return err
}

// FanOutNewEvents creates deliveries for events that lack a delivery row for an
// enabled endpoint. Avoids locking event rows so producers can insert concurrently.
func (s *Store) FanOutNewEvents(ctx context.Context) (int, error) {
	tag, err := s.Pool.Exec(ctx, `
		INSERT INTO deliveries (event_id, endpoint_id, status, next_attempt_at)
		SELECT e.id, ep.id, 'pending', now()
		FROM events e
		CROSS JOIN endpoints ep
		WHERE ep.enabled = TRUE
		  AND NOT EXISTS (
		    SELECT 1 FROM deliveries d
		    WHERE d.event_id = e.id AND d.endpoint_id = ep.id
		  )
		ON CONFLICT (event_id, endpoint_id) DO NOTHING`)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// ClaimDue claims up to limit due deliveries for a worker using SKIP LOCKED,
// respecting per-endpoint in-flight concurrency.
func (s *Store) ClaimDue(ctx context.Context, workerID string, limit int) ([]Delivery, error) {
	if limit <= 0 {
		limit = 16
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
		WITH due AS (
			SELECT d.id
			FROM deliveries d
			JOIN endpoints ep ON ep.id = d.endpoint_id
			WHERE d.status = 'pending'
			  AND d.next_attempt_at <= now()
			  AND ep.enabled = TRUE
			  AND (
			    SELECT COUNT(*) FROM deliveries d2
			    WHERE d2.endpoint_id = d.endpoint_id AND d2.status = 'in_flight'
			  ) < ep.max_concurrency
			ORDER BY d.next_attempt_at ASC
			FOR UPDATE OF d SKIP LOCKED
			LIMIT $1
		)
		UPDATE deliveries d
		SET status = 'in_flight',
		    attempt = d.attempt + 1,
		    claimed_at = now(),
		    claimed_by = $2,
		    updated_at = now()
		FROM due
		WHERE d.id = due.id
		RETURNING d.id, d.event_id, d.endpoint_id, d.attempt, d.status, d.next_attempt_at,
		          COALESCE(d.last_error,''), d.created_at`, limit, workerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var claimed []Delivery
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.ID, &d.EventID, &d.EndpointID, &d.Attempt, &d.Status,
			&d.NextAttempt, &d.LastError, &d.CreatedAt); err != nil {
			return nil, err
		}
		claimed = append(claimed, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	for i := range claimed {
		d := &claimed[i]
		if err := s.Pool.QueryRow(ctx, `
			SELECT type, payload, created_at FROM events WHERE id=$1`, d.EventID,
		).Scan(&d.EventType, &d.EventPayload, &d.EventCreated); err != nil {
			return nil, err
		}
		ep, err := s.GetEndpoint(ctx, d.EndpointID)
		if err != nil {
			return nil, err
		}
		d.Endpoint = ep
	}
	return claimed, nil
}

func (s *Store) MarkSucceeded(ctx context.Context, deliveryID int64) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var epID uuid.UUID
	if err := tx.QueryRow(ctx, `
		UPDATE deliveries SET status='succeeded', last_error=NULL, updated_at=now(), claimed_at=NULL, claimed_by=NULL
		WHERE id=$1 RETURNING endpoint_id`, deliveryID).Scan(&epID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE endpoints SET consecutive_failures=0 WHERE id=$1`, epID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) MarkFailed(ctx context.Context, deliveryID int64, nextAt time.Time, lastErr string, disableAfter int) (disabled bool, err error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	var epID uuid.UUID
	if err := tx.QueryRow(ctx, `
		UPDATE deliveries
		SET status='pending', next_attempt_at=$2, last_error=$3, updated_at=now(),
		    claimed_at=NULL, claimed_by=NULL
		WHERE id=$1
		RETURNING endpoint_id`, deliveryID, nextAt, lastErr).Scan(&epID); err != nil {
		return false, err
	}
	var failures int
	if err := tx.QueryRow(ctx, `
		UPDATE endpoints
		SET consecutive_failures = consecutive_failures + 1
		WHERE id=$1
		RETURNING consecutive_failures`, epID).Scan(&failures); err != nil {
		return false, err
	}
	if disableAfter > 0 && failures >= disableAfter {
		if _, err := tx.Exec(ctx, `UPDATE endpoints SET enabled=FALSE WHERE id=$1`, epID); err != nil {
			return false, err
		}
		disabled = true
	}
	return disabled, tx.Commit(ctx)
}

func (s *Store) MoveToDeadLetter(ctx context.Context, d Delivery, lastErr string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	body, _ := json.Marshal(map[string]any{
		"id":         d.EventID.String(),
		"type":       d.EventType,
		"payload":    json.RawMessage(d.EventPayload),
		"created_at": d.EventCreated.UTC().Format(time.RFC3339Nano),
	})

	if _, err := tx.Exec(ctx, `
		UPDATE deliveries SET status='dead', last_error=$2, updated_at=now(), claimed_at=NULL, claimed_by=NULL
		WHERE id=$1`, d.ID, lastErr); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO dead_letters (delivery_id, event_id, endpoint_id, attempts, last_error, payload)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (delivery_id) DO NOTHING`,
		d.ID, d.EventID, d.EndpointID, d.Attempt, lastErr, body); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) ListDeadLetters(ctx context.Context, limit int) ([]DeadLetter, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, delivery_id, event_id, endpoint_id, attempts, COALESCE(last_error,''), payload, created_at, replayed_at
		FROM dead_letters ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeadLetter
	for rows.Next() {
		var dl DeadLetter
		if err := rows.Scan(&dl.ID, &dl.DeliveryID, &dl.EventID, &dl.EndpointID, &dl.Attempts,
			&dl.LastError, &dl.Payload, &dl.CreatedAt, &dl.ReplayedAt); err != nil {
			return nil, err
		}
		out = append(out, dl)
	}
	return out, rows.Err()
}

func (s *Store) ReplayDeadLetter(ctx context.Context, id int64) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var deliveryID int64
	err = tx.QueryRow(ctx, `
		UPDATE dead_letters SET replayed_at=now() WHERE id=$1 AND replayed_at IS NULL
		RETURNING delivery_id`, id).Scan(&deliveryID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE deliveries
		SET status='pending', attempt=0, next_attempt_at=now(), last_error=NULL, updated_at=now()
		WHERE id=$1`, deliveryID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) CountByStatus(ctx context.Context) (map[string]int64, error) {
	rows, err := s.Pool.Query(ctx, `SELECT status, COUNT(*) FROM deliveries GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var st string
		var n int64
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}

func (s *Store) CountDeadLetters(ctx context.Context) (int64, error) {
	var n int64
	err := s.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM dead_letters`).Scan(&n)
	return n, err
}

func (s *Store) CountEvents(ctx context.Context) (int64, error) {
	var n int64
	err := s.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM events`).Scan(&n)
	return n, err
}

// RecoverStaleInFlight returns in-flight rows older than age to pending (crash recovery).
func (s *Store) RecoverStaleInFlight(ctx context.Context, age time.Duration) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE deliveries
		SET status='pending', next_attempt_at=now(), updated_at=now(), claimed_at=NULL, claimed_by=NULL
		WHERE status='in_flight' AND claimed_at < now() - $1::interval`, fmt.Sprintf("%f seconds", age.Seconds()))
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
