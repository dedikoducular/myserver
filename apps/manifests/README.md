# MyServer application manifests

Every `*.yaml` / `*.yml` file in this directory describes one application of the app store.
Adding an application means dropping a file here (and its icon into `../icons/`) and pressing
**Yenile** on the Uygulamalar page; nothing is hard-coded in the panel.

Manifests are validated strictly. A file with an unknown field, a bad value or a duplicate slug
is skipped, logged, and listed to administrators as invalid; the other applications still load.

## Top level

| Field | Required | Description |
|---|---|---|
| `schema` | no | Schema version, `1`. |
| `name` | yes | Display name, max 60 characters. |
| `slug` | yes | Unique id: `a-z`, `0-9`, `-`; 1-32 characters; starts and ends with a letter or digit. |
| `description` | yes | One-line description, max 120 characters. |
| `long_description` | no | Longer text shown in the install dialog. |
| `category` | no | `media`, `download`, `management`, `smart_home`, `tools`, `network`, `cloud`, `security`, `other` (default). |
| `icon` | no | File name in `../icons/`, `.svg` or `.png`, lower case (`[a-z0-9_-]`). |
| `version` | no | Version label shown to the user. |
| `website` | no | `http(s)://` URL. |
| `notes` | no | Install notes shown before installing. |
| `architectures` | no | CPU architectures the images support, as Go names: `amd64`, `arm64`, `arm`, `386`, `riscv64`, `ppc64le`, `s390x`. When the server's architecture is not listed the app is shown as **Desteklenmiyor** and cannot be installed. Omit it to declare nothing (no restriction). For a multi-service app list only architectures that every image supports. |
| `bind_address` | no | Default of the install dialog's access choice: `all` (default, ports published on every interface) or `loopback` (127.0.0.1 only). The user can change it. |
| `docker`, `env`, `ports`, `volumes`, `options` | | Single-service form, see below. |
| `services` | | Multi-service form: a list of services. Cannot be combined with the single-service fields. |

A single-service manifest is treated as one service named `app`.

The slug prefix `custom-` and the category `custom` ("Özel") are reserved for applications an
administrator adds from a docker-compose file in the panel; a file here that uses them is
refused. Those applications are converted into this same manifest format and stored in the
panel's database, not in this directory. See [docs/custom-apps.md](../../docs/custom-apps.md)
(Turkish) for what a compose file may contain.

## Service (`docker:` block, or one entry of `services:`)

| Field | Description |
|---|---|
| `name` | Service name (multi-service only). It is also the DNS name of the service on the application's private network. |
| `image` | Image reference, e.g. `ghcr.io/org/app:tag` (optionally `@sha256:...`). |
| `restart` | `no`, `always`, `unless-stopped` (default), `on-failure`. |
| `network_mode` | `bridge` (default; joins the private network `myserver-<slug>`), `host`, `none`. `host` is shown to the user as a warning. |
| `privileged` | `true` runs the container privileged. Discouraged; shown as a danger warning that the user must accept. |
| `cap_add` | Linux capabilities without the `CAP_` prefix, e.g. `[NET_ADMIN]`. Shown as a warning. |
| `devices` | List of `{host, container, permissions, optional}`; paths under `/dev`, permissions from `rwm`. `optional: true` skips a device the server does not have. |
| `command` | Command arguments (list). |
| `user` | `user` or `user:group` (names or numeric ids). |
| `shm_size` | `/dev/shm` size, e.g. `128m`, `1g`. |
| `tmpfs` | List of `{target, size}`. |
| `healthcheck` | `{test: ["CMD-SHELL", "..."], interval, timeout, start_period, retries}`; durations like `30s`. |
| `depends_on` | Services that must run (and be healthy, if they have a health check) before this one starts (multi-service only). |
| `env`, `ports`, `volumes`, `options` | See below (in the multi-service form they belong to the service). |

## `options`

Opt-in choices shown as switches in the install and settings dialogs. Use them for capabilities
that only some users need, so the default installation stays minimal.

| Field | Description |
|---|---|
| `key` | Id of the option, unique in the application. |
| `label` | Shown next to the switch (required). |
| `description` | Explains when the option is needed. |
| `default` | `true` enables it by default (default `false`). |
| `cap_add` | Capabilities added to the service while the option is enabled (required, at least one). |

## `env`

| Field | Description |
|---|---|
| `name` | Variable name. |
| `key` | Id of the value (default: `name`). Entries with the same key in different services share one value and one form field, e.g. a database password. |
| `label`, `description` | Shown in the form. |
| `default` | Default value. Not allowed for secrets. |
| `required` | The value must not be empty. |
| `secret` | Masked in the form, stored, never logged and never returned by the API. |
| `generate` | `password`: a random 32-character value is generated when the user leaves the (secret) field empty. |
| `fixed` | The value is set by the manifest and not shown to the user. May contain `${port:KEY}`, replaced by the host port chosen for the port with that key. |

A variable whose value is empty is not passed to the container.

## `ports`

| Field | Description |
|---|---|
| `container` | Port inside the container (1-65535). |
| `host` | Default host port (default: `container`). The user may change it. |
| `protocol` | `tcp` (default) or `udp`. |
| `key` | Id of the port (default `<service>-<container>-<protocol>`). |
| `label` | Shown in the form. |
| `web_ui` | `{scheme: http|https, path: /}` marks the port of the web interface; the panel offers **Aç**. At most one per application. |
| `same_as_host` | The container port follows the host port chosen by the user. |
| `option` | Key of an option of the same service: the port is published only while that option is enabled. |

With `network_mode: host` the ports are informational and cannot be changed.

## `volumes`

| Field | Description |
|---|---|
| `type` | `volume` (default), `bind` or `system`. |
| `source` | `volume`: volume name; the Docker volume is `myserver-<slug>-<source>`. `bind`: default host folder offered to the user (may be empty). `system`: fixed host path. |
| `target` | Absolute path inside the container. |
| `key` | Id of a `bind` volume (default derived from service and target). |
| `label`, `description` | Shown in the form. |
| `read_only` | Mount read-only. |
| `required` | `bind` only: the user must choose a folder. A non-required bind volume left empty is not mounted. |

`bind` folders are chosen by the user at install time and must be inside the file manager's
allowed roots (setting `files.allowed_roots`); system locations are always refused.

`system` mounts are the only way to mount a path outside the allowed roots (for example
`/var/run/docker.sock`). They are fixed by the manifest, shown to the user as a danger warning
and must be accepted explicitly before installing.

## Example

```yaml
name: Jellyfin
slug: jellyfin
description: Medya Sunucusu
category: media
icon: jellyfin.svg
docker:
  image: jellyfin/jellyfin:latest
  restart: unless-stopped
ports:
  - host: 8096
    container: 8096
    label: Web arayüzü
    web_ui: { scheme: http, path: / }
volumes:
  - source: config
    target: /config
  - type: bind
    key: media
    source: /media
    target: /media
    label: Medya klasörü
    required: true
```

See `immich.yaml` for a multi-service application.
