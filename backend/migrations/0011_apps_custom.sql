-- Applications an administrator added from a docker-compose file. The
-- compose file itself is not kept: it is converted into an application
-- manifest, and that manifest (YAML, the same schema as the shipped files
-- in apps/manifests) is what was shown to the administrator, what is stored
-- and what the application is installed, updated and restored from.
-- Kept in the database, not in the manifest directory, because that
-- directory belongs to the installed panel and is replaced by panel updates.
CREATE TABLE apps_custom (
    slug       TEXT PRIMARY KEY CHECK (slug LIKE 'custom-%'),
    name       TEXT NOT NULL,
    manifest   TEXT NOT NULL,
    created_by TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);
