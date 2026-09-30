package docker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/gorilla/websocket"

	"myserver/internal/auth"
	"myserver/internal/httpx"
	"myserver/internal/settings"
)

// Container terminal over WebSocket.
//
//	GET /api/v1/docker/containers/{id}/exec/ws?cols=<n>&rows=<n>   (admin)
//
// Message protocol:
//
//	client → server, binary frame : raw bytes typed by the user (stdin)
//	client → server, text frame   : JSON control message
//	    {"type":"resize","cols":120,"rows":32}
//	server → client, binary frame : raw terminal output
//	server → client, text frame   : JSON control message
//	    {"type":"ready","shell":"/bin/bash"}   the shell is attached
//	    {"type":"exit","code":0}               the shell ended
//	    {"type":"error","message":"..."}       Turkish message; the socket
//	                                           is closed right after it
//
// The exec process is ended when the socket closes: closing the attached
// connection makes the Docker daemon terminate the exec's process.
const (
	execReadLimit   = 64 << 10
	execWriteWait   = 10 * time.Second
	execPingEvery   = 30 * time.Second
	execPongWait    = 75 * time.Second
	execWatchEvery  = 30 * time.Second
	execMaxSessions = 16
)

var execUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 32 << 10,
	// The ws router has already verified the Origin header.
	CheckOrigin: func(*http.Request) bool { return true },
}

var execSessions atomic.Int32

type execControl struct {
	Type    string `json:"type"`
	Cols    uint   `json:"cols,omitempty"`
	Rows    uint   `json:"rows,omitempty"`
	Shell   string `json:"shell,omitempty"`
	Code    *int   `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// wsConn serialises writes; gorilla allows only one concurrent writer.
type wsConn struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

func (c *wsConn) write(kind int, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(execWriteWait))
	return c.conn.WriteMessage(kind, data)
}

func (c *wsConn) control(msg execControl) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return c.write(websocket.TextMessage, b)
}

// fail tells the user why the session cannot continue and closes the socket.
func (c *wsConn) fail(message string) {
	_ = c.control(execControl{Type: "error", Message: message})
	c.close(websocket.ClosePolicyViolation, "")
}

func (c *wsConn) close(code int, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(code, reason), time.Now().Add(2*time.Second))
}

func validTermSize(cols, rows uint) bool {
	return cols >= 2 && cols <= 1000 && rows >= 1 && rows <= 500
}

func querySize(r *http.Request) (cols, rows uint) {
	cols, rows = 80, 24
	c, err1 := strconv.ParseUint(r.URL.Query().Get("cols"), 10, 16)
	w, err2 := strconv.ParseUint(r.URL.Query().Get("rows"), 10, 16)
	if err1 == nil && err2 == nil && validTermSize(uint(c), uint(w)) {
		cols, rows = uint(c), uint(w)
	}
	return cols, rows
}

// pickShell prefers bash and falls back to sh, by checking which file exists
// in the container's filesystem.
func pickShell(ctx context.Context, cli *client.Client, id string) (string, error) {
	var lastErr error
	for _, shell := range []string{"/bin/bash", "/bin/sh"} {
		st, err := cli.ContainerStatPath(ctx, id, shell)
		if err == nil {
			if !st.Mode.IsDir() {
				return shell, nil
			}
			continue
		}
		if isUnavailable(err) || errors.Is(err, context.DeadlineExceeded) {
			return "", err
		}
		lastErr = err
	}
	if lastErr != nil {
		slog.Debug("konteynerde kabuk bulunamadı", "container", id, "error", lastErr.Error())
	}
	return "", nil
}

func (m *Module) terminalEnabled() bool {
	return m.deps.Settings.Bool(settings.KeyTerminalEnabled)
}

const terminalDisabledMessage = "Terminal erişimi yönetici tarafından kapatılmış."

func (m *Module) handleExec(w http.ResponseWriter, r *http.Request) {
	sess := auth.From(r.Context())
	if sess == nil || sess.User.Role != auth.RoleAdmin {
		httpx.Fail(w, r, httpx.Forbidden())
		return
	}
	id, err := pathContainerID(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	actor := sess.Actor()
	cols, rows := querySize(r)

	raw, err := execUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade has already answered
	}
	defer raw.Close()
	ws := &wsConn{conn: raw}

	// Failures after the upgrade are reported inside the socket, because a
	// browser cannot read the body of a rejected handshake.
	ctx, cancel := context.WithCancel(m.ctx)
	defer cancel()

	if !m.terminalEnabled() {
		m.deps.Audit.Log(ctx, actor, "docker.container_exec", id, "terminal erişimi kapalı", false)
		ws.fail(terminalDisabledMessage)
		return
	}
	if execSessions.Add(1) > execMaxSessions {
		execSessions.Add(-1)
		ws.fail("Çok fazla açık terminal oturumu var. Önce diğerlerini kapatın.")
		return
	}
	defer execSessions.Add(-1)

	cli, err := m.client()
	if err != nil {
		ws.fail(unavailableMessage)
		return
	}
	sctx, scancel := context.WithTimeout(ctx, listTimeout)
	defer scancel()
	insp, err := cli.ContainerInspect(sctx, id)
	if err != nil {
		ws.fail(userMessage(apiError(err, errText{notFound: containerNotFound, fallback: "Konteyner bilgileri alınamadı."}), unavailableMessage))
		return
	}
	if insp.ContainerJSONBase == nil || insp.State == nil || !insp.State.Running || insp.State.Paused {
		ws.fail("Konteyner çalışmıyor. Terminal yalnızca çalışan konteynerlerde açılabilir.")
		return
	}
	fullID, name := insp.ID, strings.TrimPrefix(insp.Name, "/")

	shell, err := pickShell(sctx, cli, fullID)
	if err != nil {
		ws.fail(userMessage(apiError(err, errText{fallback: "Terminal başlatılamadı."}), "Terminal başlatılamadı."))
		return
	}
	if shell == "" {
		m.deps.Audit.Log(ctx, actor, "docker.container_exec", name, "kabuk bulunamadı", false)
		ws.fail("Bu konteynerde kabuk (bash veya sh) bulunmuyor.")
		return
	}

	size := [2]uint{rows, cols} // [height, width]
	created, err := cli.ContainerExecCreate(sctx, fullID, container.ExecOptions{
		Tty:          true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		ConsoleSize:  &size,
		Env:          []string{"TERM=xterm-256color"},
		Cmd:          []string{shell},
	})
	if err != nil {
		m.deps.Audit.Log(ctx, actor, "docker.container_exec", name, "başarısız", false)
		ws.fail(userMessage(apiError(err, errText{
			notFound: containerNotFound,
			conflict: "Konteyner çalışmıyor veya duraklatılmış.",
			fallback: "Terminal başlatılamadı.",
		}), "Terminal başlatılamadı."))
		return
	}
	attached, err := cli.ContainerExecAttach(sctx, created.ID, container.ExecAttachOptions{Tty: true, ConsoleSize: &size})
	if err != nil {
		m.deps.Audit.Log(ctx, actor, "docker.container_exec", name, "başarısız", false)
		ws.fail(userMessage(apiError(err, errText{fallback: "Terminal başlatılamadı."}), "Terminal başlatılamadı."))
		return
	}
	// Closing the attached connection ends the exec process.
	var closeOnce sync.Once
	closeExec := func() { closeOnce.Do(attached.Close) }
	defer closeExec()

	m.deps.Audit.Log(ctx, actor, "docker.container_exec", name, "oturum açıldı: "+shell, true)
	started := time.Now()
	defer func() {
		m.deps.Audit.Log(context.WithoutCancel(ctx), actor, "docker.container_exec_end", name,
			"süre: "+time.Since(started).Round(time.Second).String(), true)
	}()

	_ = cli.ContainerExecResize(sctx, created.ID, container.ResizeOptions{Height: rows, Width: cols})
	if ws.control(execControl{Type: "ready", Shell: shell}) != nil {
		return
	}

	var lastInput atomic.Int64
	lastInput.Store(time.Now().UnixNano())

	// Output pump: container → browser.
	go func() {
		defer cancel()
		buf := make([]byte, 32<<10)
		for {
			n, rerr := attached.Reader.Read(buf)
			if n > 0 {
				if ws.write(websocket.BinaryMessage, buf[:n]) != nil {
					return
				}
			}
			if rerr != nil {
				if ctx.Err() == nil {
					if !errors.Is(rerr, io.EOF) {
						slog.Debug("terminal çıktısı okunamadı", "container", name, "error", rerr.Error())
					}
					m.reportExit(cli, ws, created.ID)
					// Do not wait long for the browser's close frame.
					_ = raw.SetReadDeadline(time.Now().Add(2 * time.Second))
				}
				return
			}
		}
	}()

	// Watchdog: keep-alive pings, session and setting re-validation, idle
	// timeout.
	go func() {
		ping := time.NewTicker(execPingEvery)
		defer ping.Stop()
		watch := time.NewTicker(execWatchEvery)
		defer watch.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ping.C:
				if ws.write(websocket.PingMessage, nil) != nil {
					cancel()
					closeExec()
					return
				}
			case <-watch.C:
				reason := ""
				vctx, vcancel := context.WithTimeout(ctx, 5*time.Second)
				active := m.deps.Auth.SessionActive(vctx, sess)
				vcancel()
				switch {
				case ctx.Err() != nil:
					return
				case !active:
					reason = "Oturumunuz sona erdi veya yetkiniz değişti. Terminal kapatıldı."
				case !m.terminalEnabled():
					reason = terminalDisabledMessage
				default:
					idle := m.deps.Settings.Int(settings.KeyTerminalTimeout, 15)
					if idle > 0 && time.Since(time.Unix(0, lastInput.Load())) > time.Duration(idle)*time.Minute {
						reason = "Terminal oturumu hareketsizlik nedeniyle kapatıldı."
					}
				}
				if reason != "" {
					cancel()
					closeExec()
					ws.fail(reason)
					_ = raw.SetReadDeadline(time.Now())
					return
				}
			}
		}
	}()

	// Input pump: browser → container. Runs until the socket closes.
	raw.SetReadLimit(execReadLimit)
	_ = raw.SetReadDeadline(time.Now().Add(execPongWait))
	raw.SetPongHandler(func(string) error {
		return raw.SetReadDeadline(time.Now().Add(execPongWait))
	})
	for {
		kind, data, rerr := raw.ReadMessage()
		if rerr != nil {
			break
		}
		if ctx.Err() != nil {
			break
		}
		_ = raw.SetReadDeadline(time.Now().Add(execPongWait))
		switch kind {
		case websocket.BinaryMessage:
			lastInput.Store(time.Now().UnixNano())
			_ = attached.Conn.SetWriteDeadline(time.Now().Add(execWriteWait))
			if _, werr := attached.Conn.Write(data); werr != nil {
				cancel()
			}
		case websocket.TextMessage:
			var msg execControl
			if json.Unmarshal(data, &msg) != nil {
				continue
			}
			if msg.Type == "resize" && validTermSize(msg.Cols, msg.Rows) {
				rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
				_ = cli.ContainerExecResize(rctx, created.ID, container.ResizeOptions{Height: msg.Rows, Width: msg.Cols})
				rcancel()
			}
		}
		if ctx.Err() != nil {
			break
		}
	}
	cancel()
	closeExec()
	ws.close(websocket.CloseNormalClosure, "")
}

// reportExit sends the shell's exit code after its output ended.
func (m *Module) reportExit(cli *client.Client, ws *wsConn, execID string) {
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	defer cancel()
	msg := execControl{Type: "exit"}
	if insp, err := cli.ContainerExecInspect(ctx, execID); err == nil && !insp.Running {
		code := insp.ExitCode
		msg.Code = &code
	}
	_ = ws.control(msg)
	ws.close(websocket.CloseNormalClosure, "")
}
