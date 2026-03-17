// Package store implements plugin.Store backed by PostgreSQL.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/lib/pq"

	"github.com/OpenStruct/peer_compute/plugin"
)

// PostgresStore implements plugin.Store using PostgreSQL.
type PostgresStore struct {
	db *sql.DB
}

// New opens a PostgreSQL connection and returns a Store.
func New(dsn string) (*PostgresStore, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping db: %w", err)
	}
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	return &PostgresStore{db: db}, nil
}

// DB exposes the underlying *sql.DB for external use (e.g., running migrations).
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

func (s *PostgresStore) GetProvider(ctx context.Context, id string) (*plugin.ProviderRecord, error) {
	return s.scanProvider(s.db.QueryRowContext(ctx,
		`SELECT id, name, address, public_address, status,
			cpu_cores, memory_mb, disk_gb, gpu_count, gpu_model,
			avail_cpu, avail_memory_mb, avail_gpu,
			registered_at, last_heartbeat
		FROM providers WHERE id = $1`, id))
}

func (s *PostgresStore) GetProviderByPrefix(ctx context.Context, prefix string) (*plugin.ProviderRecord, error) {
	// Try exact match first
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
		p := &plugin.ProviderRecord{}
		if err := scanProviderRow(rows, p); err != nil {
			return nil, err
		}
		if match != nil {
			return nil, plugin.ErrAmbiguousPrefix
		}
		match = p
	}
	if match == nil {
		return nil, plugin.ErrNotFound
	}
	return match, nil
}

func (s *PostgresStore) ListProviders(ctx context.Context, filter plugin.ProviderFilter) ([]*plugin.ProviderRecord, error) {
	var where []string
	var args []any
	n := 1

	if filter.Status != "" {
		where = append(where, fmt.Sprintf("status = $%d", n))
		args = append(args, filter.Status)
		n++
	}
	if filter.MinCPU > 0 {
		where = append(where, fmt.Sprintf("avail_cpu >= $%d", n))
		args = append(args, filter.MinCPU)
		n++
	}
	if filter.MinMemory > 0 {
		where = append(where, fmt.Sprintf("avail_memory_mb >= $%d", n))
		args = append(args, filter.MinMemory)
		n++
	}
	if filter.MinGPU > 0 {
		where = append(where, fmt.Sprintf("avail_gpu >= $%d", n))
		args = append(args, filter.MinGPU)
		n++
	}

	q := `SELECT id, name, address, public_address, status,
		cpu_cores, memory_mb, disk_gb, gpu_count, gpu_model,
		avail_cpu, avail_memory_mb, avail_gpu,
		registered_at, last_heartbeat FROM providers`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
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
			cpu_cores, memory_mb, created_at, terminated_at)
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
		sess.CPUCores, sess.MemoryMB, sess.CreatedAt, sess.TerminatedAt)
	return err
}

func (s *PostgresStore) GetSession(ctx context.Context, id string) (*plugin.SessionRecord, error) {
	return s.scanSession(s.db.QueryRowContext(ctx,
		`SELECT id, provider_id, renter_id, image, status,
			container_id, ssh_endpoint, connection_mode, relay_token,
			wg_public_key, wg_endpoint, wg_provider_ip, wg_renter_ip,
			cpu_cores, memory_mb, created_at, terminated_at
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
			cpu_cores, memory_mb, created_at, terminated_at
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
	var where []string
	var args []any
	n := 1

	if filter.ProviderID != "" {
		where = append(where, fmt.Sprintf("provider_id = $%d", n))
		args = append(args, filter.ProviderID)
		n++
	}
	if filter.RenterID != "" {
		where = append(where, fmt.Sprintf("renter_id = $%d", n))
		args = append(args, filter.RenterID)
		n++
	}
	if filter.Status != "" {
		where = append(where, fmt.Sprintf("status = $%d", n))
		args = append(args, filter.Status)
		n++
	}

	q := `SELECT id, provider_id, renter_id, image, status,
		container_id, ssh_endpoint, connection_mode, relay_token,
		wg_public_key, wg_endpoint, wg_provider_ip, wg_renter_ip,
		cpu_cores, memory_mb, created_at, terminated_at FROM sessions`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
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
		&rec.CPUCores, &rec.MemoryMB, &rec.CreatedAt, &rec.TerminatedAt)
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
		&rec.CPUCores, &rec.MemoryMB, &rec.CreatedAt, &rec.TerminatedAt)
}
