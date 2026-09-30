# MyServer module contract

Rules every feature module follows. Read this fully before writing code.

MyServer is a self-hosted server/NAS panel for **Ubuntu Server 24.04** (Debian-compatible where
possible). Go backend (single binary, native systemd service running as the unprivileged user
`myserver`), React + TypeScript + Vite frontend embedded in the binary, SQLite. The UI is
entirely **Turkish**. Priorities: stability > security > lightness > design.

It is a real product, not a demo.

## Hard rules

- No mock/fake/sample data, no placeholder functions, no TODOs standing in for features, no UI
  control that does nothing. If something cannot be determined on a host (no temperature sensor,
  no SMART support), return `null`/an explicit "unsupported" state and show that honestly.
- Never run a shell with user input. No `sh -c`. Use `exec.CommandContext` with an absolute
  binary path and separate arguments, put `--` before user-supplied operands, validate every
  value against a strict pattern or allow-list first.
- Prefer `/proc`, `/sys` and Go libraries over spawning commands. Cache expensive reads briefly.
  Nothing may poll expensive commands in a tight loop.
- Live data goes over SSE or WebSocket, not HTTP polling.
- Never log or return secrets (passwords, tokens, env values marked secret).
- Errors shown to users are specific Turkish sentences ("Docker servisine ulaşılamıyor.",
  "Disk bağlı değil."). Never expose stack traces or raw internal errors.
- Record every state-changing action in the audit log.
- Dangerous operations (data loss, formatting, firewall changes, anything that can cut SSH
  access) must only ever run when the end user explicitly triggers them in the UI, behind a
  confirmation. **During development never execute them** — write the code, do not run it.
- Do not copy CasaOS code, names or logos.

## Files you may and may not touch

You own only:

- `backend/internal/<module>/**`
- `backend/internal/helper/actions_<module>.go` (root helper actions, if needed)
- `backend/migrations/<assigned number>_<module>.sql` (if needed)
- `frontend/src/modules/<module>/**`
- anything else explicitly assigned in your task

Do **not** edit shared files: `go.mod`, `go.sum`, `package.json`, `package-lock.json`,
`backend/cmd/**`, `backend/internal/{httpx,auth,settings,module,server,privileged,notify,audit,
logbuf,config,database,webui,deps}/**`, `backend/internal/helper/helper.go`,
`frontend/src/{components,hooks,services,stores,lib,i18n,types,layouts,pages,styles}/**`,
`frontend/src/App.tsx`, `frontend/src/main.tsx`. Do not run `go get`, `go mod tidy` or
`npm install`. All dependencies you may use are already installed (see below). If you believe a
shared file needs a change, do not make it — describe it in your final report.

Other modules are being written at the same time by other engineers. Do not import another
feature module's Go package or frontend module unless your task says so.

## Environment

Development happens on Windows; the product runs on Linux. You cannot run the backend here.

```bash
export PATH="$LOCALAPPDATA/myserver-toolchain/go/bin:$PATH"
cd backend
gofmt -l ./internal/<module>                       # must print nothing
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...
GOOS=linux GOARCH=amd64 go vet ./internal/<module>/... ./internal/helper/...

cd ../frontend
npx tsc -b --noEmit        # only errors in YOUR files are your concern
```

Both must pass for your files before you finish. Other modules may be mid-edit, so a build error
located in someone else's package is not yours to fix — re-run a little later, and build your own
package alone with `go build ./internal/<module>/...` to confirm yours is clean.

Put Linux-only code (syscalls, `golang.org/x/sys/unix`, PTY) in files ending `_linux.go` or with
`//go:build linux`, and keep pure logic (parsers, validators, path checks) in portable files so
it can be unit-tested on the development machine. Parsers of `/proc`, `lsblk`, `systemctl` etc.
output must take the text/bytes as input rather than reading the system themselves.
Do **not** write tests now; tests are written in a later phase.

A pre-tool hook ("GateGuard") rejects the first shell command and the first write of each new
file until you have stated, in your message text, (1) which files will import/call the new file,
(2) that no existing file serves the purpose, (3) any data schema involved, (4) the instruction
you are following. State those facts briefly for the batch of files and retry the same call.

Available Go dependencies: standard library, `github.com/docker/docker` (v28 client, plus its
`api/types/...` packages), `github.com/gorilla/websocket`, `github.com/creack/pty`,
`golang.org/x/sys/unix`, `golang.org/x/crypto`, `gopkg.in/yaml.v3`, `modernc.org/sqlite` (via
`database/sql`; already opened for you).

Available frontend dependencies: `react` 19, `react-router-dom` 7, `zustand`, `lucide-react`
(icons — the only icon set), `@xterm/xterm`, `@xterm/addon-fit`, Tailwind CSS v4.

## Backend

### Module shape

```go
package storage // backend/internal/storage

func New(deps module.Deps, st *settings.API) (module.Module, error)

func (m *Module) Name() string { return "storage" }

// api: /api/v1 with session + CSRF enforced. ws: /api/v1 with session + Origin check, for
// WebSocket upgrades (browsers cannot send the CSRF header on a handshake).
func (m *Module) Register(api, ws *httpx.Router) {
    g := api.Group("/storage")                      // any signed-in user
    g.Get("/disks", m.handleDisks)
    a := api.Group("/storage", auth.RequireAdmin)   // admin only: everything that changes state
    a.Post("/mount", m.handleMount)
}

// Optional: background work. Must return when ctx is cancelled.
func (m *Module) Start(ctx context.Context)

// Optional: contributes to the health monitor. Must be cheap or cached.
func (m *Module) Health(ctx context.Context) []module.HealthCheck
```

Read these files before starting: `backend/internal/module/module.go`,
`backend/internal/httpx/httpx.go`, `backend/internal/httpx/router.go`,
`backend/internal/privileged/privileged.go`, `backend/internal/helper/helper.go`,
`backend/internal/helper/actions_system.go`, `backend/internal/settings/store.go`,
`backend/internal/settings/handlers.go`, `backend/internal/notify/notify.go`,
`backend/internal/audit/audit.go`, `backend/internal/auth/service.go`.

`New` must not fail just because a system facility is missing (Docker stopped, tool not
installed). Construct anyway and report the condition per request, so the panel stays up.

### Handlers

```go
func (m *Module) handleMount(w http.ResponseWriter, r *http.Request) error {
    var req struct{ Device string `json:"device"` }
    if err := httpx.Decode(w, r, &req); err != nil { return err }   // 1 MiB cap, unknown fields rejected
    if !validDevice(req.Device) { return httpx.BadRequest("Disk adı geçersiz.") }
    if _, err := m.deps.Priv.Run(r.Context(), "storage-mount", req.Device); err != nil {
        m.deps.Audit.Log(r.Context(), auth.ActorFrom(r), "storage.mount", req.Device, "başarısız", false)
        return httpx.NewError(http.StatusBadGateway, "mount_failed",
            privileged.UserMessage(err, "Disk bağlanamadı.")).Wrap(err)
    }
    m.deps.Audit.Log(r.Context(), auth.ActorFrom(r), "storage.mount", req.Device, "", true)
    httpx.OK(w, result)
    return nil
}
```

- Success: `httpx.OK(w, data)` / `httpx.JSON(w, status, data)`. Failure: `return` an
  `*httpx.Error`. Any other returned error becomes a generic 500 and is logged.
  Envelope: `{"success":true,"data":...,"error":null}` /
  `{"success":false,"data":null,"error":{"code":"...","message":"..."}}`.
- JSON field names are `snake_case`. Times are Unix seconds (integers). Sizes are bytes.
  Return `[]` not `null` for empty lists.
- Path parameters: `r.PathValue("id")` (Go 1.22+ mux patterns, e.g. `g.Get("/containers/{id}", h)`).
- Audit action names are `module.verb` (`docker.container_restart`, `apps.install`).
- Notifications: `m.deps.Notify.Publish(ctx, notify.Warning, "storage", "Başlık", "Mesaj")`; for a
  recurring condition use `PublishOnce(..., dedupeKey, window)`.
- Log with `log/slog` (`slog.Info/Warn/Error`), structured key/values, Turkish message.
- SSE: set `Content-Type: text/event-stream`, flush after each event, send a `: ping` comment
  every ~25 s, stop on `r.Context().Done()`. See `handleNotificationStream` in
  `backend/internal/server/core.go`. Register SSE on the `api` router (it is a GET).
- WebSocket: register on the `ws` router with `ws.Raw("GET", "/terminal/ws", handler)`; the
  Origin has already been verified, so the upgrader's `CheckOrigin` may return true. Check
  `auth.From(r.Context()).User.Role` yourself for admin-only sockets. Set read limits and
  ping/pong deadlines.
- Settings owned by the module: `settings.RegisterDefault(key, value)` and
  `st.Allow(key, validator, applier)` inside `New`. Key names are `<module>.<name>`.

### Privileged operations

The panel process is not root. It has: membership of the `docker` group, read access to `/proc`
and `/sys`, and the capabilities `CAP_DAC_OVERRIDE`, `CAP_DAC_READ_SEARCH`, `CAP_CHOWN`,
`CAP_FOWNER` (so the file manager can work in users' directories). Anything else that needs root
goes through the helper:

```go
out, err := deps.Priv.Run(ctx, "storage-mount", device, mountpoint)
```

which runs `sudo -n /usr/local/libexec/myserver-helper storage-mount <device> <mountpoint>`.
Add actions in `backend/internal/helper/actions_<module>.go`:

```go
func init() {
    Register("storage-mount", func(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
        if err := ArgCount(args, 2); err != nil { return err }
        // Validate EVERYTHING again here. The helper is the security boundary and must assume
        // the caller is hostile.
        return Exec(ctx, stdout, "/usr/bin/mount", "--", args[0], args[1])
    })
}
```

- Action names: `<module>-<verb>`, lowercase, digits and `-` only.
- Return `helper.Userf("Türkçe mesaj")` for a failure the user should read; the panel retrieves
  it with `privileged.UserMessage(err, fallback)`.
- Actions must be narrow. Never add an action that runs an arbitrary command, writes an
  arbitrary file, or takes a path without restricting where it may point.
- Read-only information that needs root (e.g. `smartctl`) also goes through a helper action.
- The helper package may import `myserver/internal/settings` and the standard library, plus
  portable validation code from your own module package **only if** that package does not import
  `module`, `auth` or `server` (to avoid cycles, keep shared validators in a small sub-package
  such as `internal/<module>/<module>check`).

### Database

Add `backend/migrations/<number>_<module>.sql` using the number assigned in your task. Plain
SQLite DDL, applied once in a transaction. Table names are prefixed with the module name.

## Frontend

Read before starting: `frontend/src/components/ui/primitives.tsx`, `overlay.tsx`, `charts.tsx`,
`feedback.tsx`, `frontend/src/services/api.ts`, `frontend/src/hooks/useApi.ts`,
`frontend/src/hooks/useStream.ts`, `frontend/src/lib/format.ts`, `frontend/src/i18n/index.ts`,
`frontend/src/stores/auth.ts`, `frontend/src/stores/ui.ts`, `frontend/src/styles/index.css`.
Look at the design reference: `docs/design/dashboard-reference.png`.

### Module shape

`frontend/src/modules/<module>/index.tsx`:

```tsx
export default function StoragePage() { ... }        // the module's page, if it has one
export function StorageWidget() { ... }              // dashboard widget(s), if assigned
export function StorageSettingsSection() { ... }     // settings section, if assigned
```

Split into more files inside your module folder as needed (`api.ts`, `types.ts`, components).
Import shared code through the `@/` alias (`@/components/ui`, `@/services/api`, ...).

- Data: `useQuery<T>('/storage/disks')`, mutations through `useAction(() => api.post(...))`,
  streams through `useEventSource` / `useSocket`. Do not write your own fetch wrapper.
- Feedback: `toast.success(...)` / `toast.error(...)` from `@/stores/ui` after actions.
- Every data view handles four states: loading (`LoadingState`/`Skeleton`), error (`ErrorState`
  with retry), empty (`EmptyState`), and data.
- Admin-only controls: `const isAdmin = useAuth((s) => s.isAdmin)`; hide or disable them for
  other users (the backend enforces it regardless).
- Destructive actions use `ConfirmDialog` with `danger`; irreversible ones also use
  `requireText`.
- Strings: define them with `messages({ tr: { ... } })` from `@/i18n` in your module and use
  `t('key')`. No hard-coded user-facing text in JSX other than through `t`.
- Formatting: use `@/lib/format` (`formatBytes`, `formatPercent`, `formatDuration`, ...).
- Links between pages: `react-router-dom` `Link`/`useNavigate`. Routes are `/`, `/apps`,
  `/docker`, `/files`, `/storage`, `/network`, `/services`, `/terminal`, `/backup`, `/updates`,
  `/settings`.

### Design

Follow `docs/design/dashboard-reference.png`: dark navy/anthracite surfaces, blue accent with
light cyan, rounded cards with clean borders, very subtle glow, professional, minimal animation.
No cartoon look, no heavy gradients.

- Use only the token colors through Tailwind classes: `bg-bg`, `bg-surface`, `bg-card`,
  `bg-raised`, `border-line`, `border-line-strong`, `text-fg`, `text-muted`, `text-faint`,
  `accent`, `accent-strong`, `cyan`, `success`, `warning`, `danger`, `purple` (e.g.
  `text-success`, `bg-accent/12`). Never write literal hex/rgb colors or Tailwind palette colors
  such as `bg-slate-800` — the panel has a light theme that depends on the tokens.
- Build from the shared components (`Card`, `CardHeader`, `Button`, `IconButton`, `Badge`,
  `Status`, `IconTile`, `ProgressBar`, `Tabs`, `Modal`, `ConfirmDialog`, `Menu`, `Field`,
  `Input`, `Select`, `Switch`, `Sparkline`, `AreaChart`, `KeyValueList`, `TableWrap`,
  `tableClass`, `PageHeader`, `Alert`) rather than restyling from scratch.
- Dashboard widgets render a `Card` with a `CardHeader` and a "Tümünü Gör" link to the module
  page where the reference shows one. They must be compact and must not assume a fixed width.
- Responsive: desktop first, but fully usable on a phone (≥ 360 px wide). Tables either scroll
  horizontally inside `TableWrap` or collapse to stacked cards below `sm`. Touch targets are at
  least 40 px. No horizontal page scrolling.
- Accessible: real `<button>`s, labels on inputs, `aria-label` on icon-only buttons, visible
  focus.

## Final report

End with a short report containing:

1. Files created.
2. API endpoints (method, path, admin-only or not, request and response shape).
3. Helper actions added, with arguments.
4. Settings keys registered (key, default, meaning).
5. Frontend exports (page, widgets, settings section) with their exact names.
6. System packages/binaries the module needs at runtime (for the installer).
7. Anything unverified, any shortcut taken, and any change you need in a shared file.

Be exact and honest in the report: say plainly what could not be verified on this machine.
