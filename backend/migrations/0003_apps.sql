-- Installed applications. One row per application installed through the
-- application system. Read by the backup module.
CREATE TABLE apps_installed (
    slug         TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    version      TEXT NOT NULL DEFAULT '',
    -- JSON: the resolved configuration (apps.Config) the containers were
    -- created from: services, images, ports, environment, volumes and bind
    -- paths. Contains secret values; never return it unmasked.
    config       TEXT NOT NULL,
    -- JSON: the values the user chose (apps.Inputs): ports, env, paths.
    -- Contains secret values.
    inputs       TEXT NOT NULL,
    -- JSON: service name -> {"image","id","digest"} of the image in use.
    images       TEXT NOT NULL DEFAULT '{}',
    installed_at INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);

-- Values kept after an application was removed while its data volumes were
-- kept, so that a later reinstall reuses generated secrets (for example a
-- database password already written into the kept volume).
CREATE TABLE apps_retained (
    slug       TEXT PRIMARY KEY,
    inputs     TEXT NOT NULL,
    removed_at INTEGER NOT NULL
);
