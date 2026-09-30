-- Application backups. One row per backup archive (and per attempt: failed
-- and cancelled attempts are kept so that the history is honest).
CREATE TABLE backup_backups (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    slug            TEXT NOT NULL,
    app_name        TEXT NOT NULL DEFAULT '',
    app_version     TEXT NOT NULL DEFAULT '',
    -- File name inside <backup dir>/<slug>/; empty while no file exists.
    file_name       TEXT NOT NULL DEFAULT '',
    size            INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL,
    -- Seconds.
    duration        INTEGER NOT NULL DEFAULT 0,
    -- running | success | failed | cancelled
    status          TEXT NOT NULL,
    -- stopped: the application was not running while its data was copied.
    -- live: it was running; databases may be inconsistent.
    consistency     TEXT NOT NULL,
    -- 1 when the backup stopped a running application (used to start it
    -- again if the panel was interrupted in the middle of the backup).
    app_was_running INTEGER NOT NULL DEFAULT 0,
    encrypted       INTEGER NOT NULL DEFAULT 0,
    includes_binds  INTEGER NOT NULL DEFAULT 1,
    -- manual | scheduled | safety (taken before a restore) | imported
    trigger_kind    TEXT NOT NULL,
    error           TEXT NOT NULL DEFAULT '',
    -- JSON array of Turkish warning sentences.
    warnings        TEXT NOT NULL DEFAULT '[]',
    -- Last on-demand verification: time and result (0 = never verified).
    verified_at     INTEGER NOT NULL DEFAULT 0,
    verify_ok       INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX backup_backups_slug ON backup_backups (slug, created_at);

-- One schedule per application. Times are in the server's local time zone.
CREATE TABLE backup_schedules (
    slug          TEXT PRIMARY KEY,
    enabled       INTEGER NOT NULL DEFAULT 0,
    -- daily | weekly | monthly
    frequency     TEXT NOT NULL DEFAULT 'daily',
    hour          INTEGER NOT NULL DEFAULT 3,
    minute        INTEGER NOT NULL DEFAULT 0,
    -- 0 = Sunday ... 6 = Saturday (weekly).
    weekday       INTEGER NOT NULL DEFAULT 1,
    -- 1..31 (monthly); months that are shorter use their last day.
    monthday      INTEGER NOT NULL DEFAULT 1,
    -- Keep the newest N backups.
    keep_last     INTEGER NOT NULL DEFAULT 7,
    -- 1: manual backups are counted and deleted by retention too.
    prune_manual  INTEGER NOT NULL DEFAULT 0,
    -- 1: back up while the application runs.
    live          INTEGER NOT NULL DEFAULT 0,
    include_binds INTEGER NOT NULL DEFAULT 1,
    -- Start of the last scheduled run, 0 when it never ran.
    last_run_at   INTEGER NOT NULL DEFAULT 0,
    updated_at    INTEGER NOT NULL
);
