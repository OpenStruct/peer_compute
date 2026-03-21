// Package store provides a PostgreSQL-backed implementation of plugin.Store.
// This is a public package intended for use by both the open-source registry
// and downstream projects (e.g. peer_compute_pro).
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/lib/pq"

	"github.com/OpenStruct/peer_compute/plugin"
)

// psql is a squirrel statement builder configured for PostgreSQL $1-style placeholders.
var psql = sq.StatementBuilder.PlaceholderFormat(sq.Dollar)

// PostgresStore implements plugin.Store using PostgreSQL.
type PostgresStore struct {
	db *sql.DB
}

// New opens a PostgreSQL connection and returns a PostgresStore.
func New(dsn string) (*PostgresStore, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping db: %w", err)
	}
	db.SetMaxOpenConns(envInt("DB_MAX_OPEN_CONNS", 50))
	db.SetMaxIdleConns(envInt("DB_MAX_IDLE_CONNS", 25))
	db.SetConnMaxLifetime(envDuration("DB_CONN_MAX_LIFETIME", 30*time.Minute))
	db.SetConnMaxIdleTime(envDuration("DB_CONN_MAX_IDLE_TIME", 5*time.Minute))
	return &PostgresStore{db: db}, nil
}

// envInt reads an integer from the environment, returning def if unset or invalid.
func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// envDuration reads a duration (in seconds) from the environment, returning def if unset.
func envDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return time.Duration(n) * time.Second
}

// NewFromDB wraps an existing *sql.DB. Intended for testing with sqlmock.
func NewFromDB(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

// DB exposes the underlying *sql.DB for callers that need direct access
// (e.g. running migrations, billing queries).
func (s *PostgresStore) DB() *sql.DB { return s.db }

func (s *PostgresStore) Close() error { return s.db.Close() }

// ── ProviderStore ──────────────────────────────────────────────────

func (s *PostgresStore) PutProvider(ctx context.Context, p *plugin.ProviderRecord) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO providers (id, name, address, public_address, status,
			cpu_cores, memory_mb, disk_gb, gpu_count, gpu_model,
			avail_cpu, avail_memory_mb, avail_gpu,
			registered_at, last_heartbeat)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		ON CONFLICT (id) DO UPDATE SET
			name=EXCLUDED.name, address=EXCLUDED.address, public_address=EXCLUDED.public_address,
			status=EXCLUDED.status, cpu_cores=EXCLUDED.cpu_cores, memory_mb=EXCLUDED.memory_mb,
			disk_gb=EXCLUDED.disk_gb, gpu_count=EXCLUDED.gpu_count, gpu_model=EXCLUDED.gpu_model,
			avail_cpu=EXCLUDED.avail_cpu, avail_memory_mb=EXCLUDED.avail_memory_mb,
			avail_gpu=EXCLUDED.avail_gpu, last_heartbeat=EXCLUDED.last_heartbeat`,
		p.ID, p.Name, p.Address, p.PublicAddress, p.Status,
		p.CPUCores, p.MemoryMB, p.DiskGB, p.GPUCount, p.GPUModel,
		p.AvailCPU, p.AvailMemoryMB, p.AvailGPU,
		p.RegisteredAt, p.LastHeartbeat)
	return err
}

// UpdateHeartbeat performs a lightweight heartbeat update, setting only
// the last_heartbeat timestamp and status to 'online'. This avoids the
// overhead of the full UPSERT that PutProvider performs.
func (s *PostgresStore) UpdateHeartbeat(ctx context.Context, providerID string, heartbeat int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE providers SET last_heartbeat = $1, status = 'online' WHERE id = $2`,
		heartbeat, providerID)
	return err
}

func (s *PostgresStore) GetProvider(ctx context.Context, id string) (*plugin.ProviderRecord, error) {
	return s.scanProvider(s.db.QueryRowContext(ctx,
		`SELECT id, name, address, public_address, status,
			cpu_cores, memory_mb, disk_gb, gpu_count, gpu_model,
			avail_cpu, avail_memory_mb, avail_gpu,
			registered_at, last_heartbeat
		FROM providers WHERE id = $1`, id))
}

func (s *PostgresStore) GetProviderByPrefix(ctx context.Context, prefix string) (*plugin.ProviderRecord, error) {
	p, err := s.GetProvider(ctx, prefix)
	if err == nil {
		return p, nil
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, address, public_address, status,
			cpu_cores, memory_mb, disk_gb, gpu_count, gpu_model,
			avail_cpu, avail_memory_mb, avail_gpu,
			registered_at, last_heartbeat
		FROM providers WHERE id LIKE $1 LIMIT 2`, prefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var match *plugin.ProviderRecord
	for rows.Next() {
		rec := &plugin.ProviderRecord{}
		if err := scanProviderRow(rows, rec); err != nil {
			return nil, err
		}
		if match != nil {
			return nil, plugin.ErrAmbiguousPrefix
		}
		match = rec
	}
	if match == nil {
		return nil, plugin.ErrNotFound
	}
	return match, nil
}

func (s *PostgresStore) ListProviders(ctx context.Context, filter plugin.ProviderFilter) ([]*plugin.ProviderRecord, error) {
	qb := psql.Select("id, name, address, public_address, status, cpu_cores, memory_mb, disk_gb, gpu_count, gpu_model, avail_cpu, avail_memory_mb, avail_gpu, registered_at, last_heartbeat").
		From("providers")

	if filter.Status != "" {
		qb = qb.Where(sq.Eq{"status": filter.Status})
	}
	if filter.MinCPU > 0 {
		qb = qb.Where(sq.GtOrEq{"avail_cpu": filter.MinCPU})
	}
	if filter.MinMemory > 0 {
		qb = qb.Where(sq.GtOrEq{"avail_memory_mb": filter.MinMemory})
	}
	if filter.MinGPU > 0 {
		qb = qb.Where(sq.GtOrEq{"avail_gpu": filter.MinGPU})
	}

	q, args, err := qb.ToSql()
	if err != nil {
		return nil, fmt.Errorf("build provider query: %w", err)
	}

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*plugin.ProviderRecord
	for rows.Next() {
		p := &plugin.ProviderRecord{}
		if err := scanProviderRow(rows, p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *PostgresStore) DeleteProvider(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM providers WHERE id = $1`, id)
	return err
}

// ── SessionStore ───────────────────────────────────────────────────

func (s *PostgresStore) PutSession(ctx context.Context, sess *plugin.SessionRecord) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions (id, provider_id, renter_id, image, status,
			container_id, ssh_endpoint, connection_mode, relay_token,
			wg_public_key, wg_endpoint, wg_provider_ip, wg_renter_ip,
			cpu_cores, memory_mb,
			created_at, terminated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		ON CONFLICT (id) DO UPDATE SET
			status=EXCLUDED.status, container_id=EXCLUDED.container_id,
			ssh_endpoint=EXCLUDED.ssh_endpoint, connection_mode=EXCLUDED.connection_mode,
			wg_public_key=EXCLUDED.wg_public_key, wg_endpoint=EXCLUDED.wg_endpoint,
			wg_provider_ip=EXCLUDED.wg_provider_ip, wg_renter_ip=EXCLUDED.wg_renter_ip,
			terminated_at=EXCLUDED.terminated_at`,
		sess.ID, sess.ProviderID, sess.RenterID, sess.Image, sess.Status,
		sess.ContainerID, sess.SSHEndpoint, sess.ConnectionMode, sess.RelayToken,
		sess.WGPublicKey, sess.WGEndpoint, sess.WGProviderIP, sess.WGRenterIP,
		sess.CPUCores, sess.MemoryMB,
		sess.CreatedAt, sess.TerminatedAt)
	return err
}

func (s *PostgresStore) GetSession(ctx context.Context, id string) (*plugin.SessionRecord, error) {
	return s.scanSession(s.db.QueryRowContext(ctx,
		`SELECT id, provider_id, renter_id, image, status,
			container_id, ssh_endpoint, connection_mode, relay_token,
			wg_public_key, wg_endpoint, wg_provider_ip, wg_renter_ip,
			cpu_cores, memory_mb,
			created_at, terminated_at
		FROM sessions WHERE id = $1`, id))
}

func (s *PostgresStore) GetSessionByPrefix(ctx context.Context, prefix string) (*plugin.SessionRecord, error) {
	sess, err := s.GetSession(ctx, prefix)
	if err == nil {
		return sess, nil
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, provider_id, renter_id, image, status,
			container_id, ssh_endpoint, connection_mode, relay_token,
			wg_public_key, wg_endpoint, wg_provider_ip, wg_renter_ip,
			cpu_cores, memory_mb,
			created_at, terminated_at
		FROM sessions WHERE id LIKE $1 LIMIT 2`, prefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var match *plugin.SessionRecord
	for rows.Next() {
		rec := &plugin.SessionRecord{}
		if err := scanSessionRow(rows, rec); err != nil {
			return nil, err
		}
		if match != nil {
			return nil, plugin.ErrAmbiguousPrefix
		}
		match = rec
	}
	if match == nil {
		return nil, plugin.ErrNotFound
	}
	return match, nil
}

func (s *PostgresStore) ListSessions(ctx context.Context, filter plugin.SessionFilter) ([]*plugin.SessionRecord, error) {
	qb := psql.Select("id, provider_id, renter_id, image, status, container_id, ssh_endpoint, connection_mode, relay_token, wg_public_key, wg_endpoint, wg_provider_ip, wg_renter_ip, cpu_cores, memory_mb, created_at, terminated_at").
		From("sessions")

	if filter.ProviderID != "" {
		qb = qb.Where(sq.Eq{"provider_id": filter.ProviderID})
	}
	if filter.RenterID != "" {
		qb = qb.Where(sq.Eq{"renter_id": filter.RenterID})
	}
	if filter.Status != "" {
		qb = qb.Where(sq.Eq{"status": filter.Status})
	}

	q, args, err := qb.ToSql()
	if err != nil {
		return nil, fmt.Errorf("build session query: %w", err)
	}

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*plugin.SessionRecord
	for rows.Next() {
		rec := &plugin.SessionRecord{}
		if err := scanSessionRow(rows, rec); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (s *PostgresStore) DeleteSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = $1`, id)
	return err
}

func (s *PostgresStore) GetTerminatedSessionIDs(ctx context.Context, sessionIDs []string, providerID string) ([]string, error) {
	if len(sessionIDs) == 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM sessions WHERE id = ANY($1) AND provider_id = $2 AND status = 'terminated'`,
		pq.Array(sessionIDs), providerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var terminated []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		terminated = append(terminated, id)
	}
	return terminated, rows.Err()
}

// ── Scan helpers ───────────────────────────────────────────────────

func (s *PostgresStore) scanProvider(row *sql.Row) (*plugin.ProviderRecord, error) {
	p := &plugin.ProviderRecord{}
	err := row.Scan(
		&p.ID, &p.Name, &p.Address, &p.PublicAddress, &p.Status,
		&p.CPUCores, &p.MemoryMB, &p.DiskGB, &p.GPUCount, &p.GPUModel,
		&p.AvailCPU, &p.AvailMemoryMB, &p.AvailGPU,
		&p.RegisteredAt, &p.LastHeartbeat)
	if err == sql.ErrNoRows {
		return nil, plugin.ErrNotFound
	}
	return p, err
}

func scanProviderRow(rows *sql.Rows, p *plugin.ProviderRecord) error {
	return rows.Scan(
		&p.ID, &p.Name, &p.Address, &p.PublicAddress, &p.Status,
		&p.CPUCores, &p.MemoryMB, &p.DiskGB, &p.GPUCount, &p.GPUModel,
		&p.AvailCPU, &p.AvailMemoryMB, &p.AvailGPU,
		&p.RegisteredAt, &p.LastHeartbeat)
}

func (s *PostgresStore) scanSession(row *sql.Row) (*plugin.SessionRecord, error) {
	rec := &plugin.SessionRecord{}
	err := row.Scan(
		&rec.ID, &rec.ProviderID, &rec.RenterID, &rec.Image, &rec.Status,
		&rec.ContainerID, &rec.SSHEndpoint, &rec.ConnectionMode, &rec.RelayToken,
		&rec.WGPublicKey, &rec.WGEndpoint, &rec.WGProviderIP, &rec.WGRenterIP,
		&rec.CPUCores, &rec.MemoryMB,
		&rec.CreatedAt, &rec.TerminatedAt)
	if err == sql.ErrNoRows {
		return nil, plugin.ErrNotFound
	}
	return rec, err
}

func scanSessionRow(rows *sql.Rows, rec *plugin.SessionRecord) error {
	return rows.Scan(
		&rec.ID, &rec.ProviderID, &rec.RenterID, &rec.Image, &rec.Status,
		&rec.ContainerID, &rec.SSHEndpoint, &rec.ConnectionMode, &rec.RelayToken,
		&rec.WGPublicKey, &rec.WGEndpoint, &rec.WGProviderIP, &rec.WGRenterIP,
		&rec.CPUCores, &rec.MemoryMB,
		&rec.CreatedAt, &rec.TerminatedAt)
}
