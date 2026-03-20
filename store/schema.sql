CREATE TABLE IF NOT EXISTS providers (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    address         TEXT NOT NULL,
    public_address  TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'offline',
    cpu_cores       INT NOT NULL DEFAULT 0,
    memory_mb       BIGINT NOT NULL DEFAULT 0,
    disk_gb         BIGINT NOT NULL DEFAULT 0,
    gpu_count       INT NOT NULL DEFAULT 0,
    gpu_model       TEXT NOT NULL DEFAULT '',
    avail_cpu       INT NOT NULL DEFAULT 0,
    avail_memory_mb BIGINT NOT NULL DEFAULT 0,
    avail_gpu       INT NOT NULL DEFAULT 0,
    registered_at   BIGINT NOT NULL,
    last_heartbeat  BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    id              TEXT PRIMARY KEY,
    provider_id     TEXT NOT NULL REFERENCES providers(id),
    renter_id       TEXT NOT NULL,
    image           TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending',
    container_id    TEXT NOT NULL DEFAULT '',
    ssh_endpoint    TEXT NOT NULL DEFAULT '',
    connection_mode TEXT NOT NULL DEFAULT '',
    relay_token     TEXT NOT NULL DEFAULT '',
    wg_public_key   TEXT NOT NULL DEFAULT '',
    wg_endpoint     TEXT NOT NULL DEFAULT '',
    wg_provider_ip  TEXT NOT NULL DEFAULT '',
    wg_renter_ip    TEXT NOT NULL DEFAULT '',
    cpu_cores       INT NOT NULL DEFAULT 0,
    memory_mb       BIGINT NOT NULL DEFAULT 0,
    created_at      BIGINT NOT NULL,
    terminated_at   BIGINT NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_sessions_provider ON sessions(provider_id);
CREATE INDEX IF NOT EXISTS idx_sessions_renter ON sessions(renter_id);
CREATE INDEX IF NOT EXISTS idx_sessions_status ON sessions(status);
