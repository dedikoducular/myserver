-- Last known result of each update check (apt, docker, self), as JSON.
CREATE TABLE updates_state (
    area       TEXT PRIMARY KEY,
    checked_at INTEGER NOT NULL,
    payload    TEXT NOT NULL
);

-- History of update jobs (package upgrades, image pulls) with their output.
CREATE TABLE updates_jobs (
    id          TEXT PRIMARY KEY,
    kind        TEXT NOT NULL,
    title       TEXT NOT NULL,
    detail      TEXT NOT NULL DEFAULT '',
    username    TEXT NOT NULL DEFAULT '',
    started_at  INTEGER NOT NULL,
    finished_at INTEGER,
    status      TEXT NOT NULL CHECK (status IN ('running', 'success', 'failed')),
    message     TEXT NOT NULL DEFAULT '',
    log         TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_updates_jobs_started ON updates_jobs(started_at);
