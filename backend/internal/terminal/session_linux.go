//go:build linux

package terminal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
	"golang.org/x/sys/unix"

	"myserver/internal/auth"
	"myserver/internal/httpx"
	"myserver/internal/privileged"
	"myserver/internal/settings"
	"myserver/internal/terminal/termcheck"
)

const (
	// readLimit bounds one client frame. The client splits pasted text into
	// smaller frames.
	readLimit   = 64 << 10
	outputChunk = 32 << 10
	writeWait   = 10 * time.Second
	pongWait    = 70 * time.Second
	pingEvery   = 25 * time.Second

	startWait  = 15 * time.Second
	flushWait  = time.Second
	killGrace  = 5 * time.Second
	reapWait   = 12 * time.Second
	stderrKeep = 16 << 10
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4 << 10,
	WriteBufferSize: outputChunk,
	// The ws router has already verified the Origin header.
	CheckOrigin: func(*http.Request) bool { return true },
}

// Reasons a session ends, also used as the "code" of the final message.
const (
	endExit        = "exit"
	endClient      = "client_closed"
	endSlow        = "client_timeout"
	endDisabled    = "terminal_disabled"
	endUserChanged = "user_changed"
	endSession     = "session_expired"
	endIdle        = "idle_timeout"
	endMaxLength   = "max_length"
	endShutdown    = "shutdown"
	endIO          = "io_error"
)

type serverMessage struct {
	Type    string `json:"type"`
	Code    *int   `json:"code,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
	Seconds *int   `json:"seconds,omitempty"`
}

type clientMessage struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

func encode(m serverMessage) []byte {
	b, _ := json.Marshal(m)
	return b
}

// markerBuffer keeps the helper's stderr (bounded) and reports when the
// helper announces that the shell has started.
type markerBuffer struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	started chan struct{}
	once    sync.Once
}

func (b *markerBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	if room := stderrKeep - b.buf.Len(); room > 0 {
		q := p
		if len(q) > room {
			q = q[:room]
		}
		b.buf.Write(q)
	}
	seen := bytes.Contains(b.buf.Bytes(), []byte(termcheck.StartedMarker))
	b.mu.Unlock()
	if seen {
		b.once.Do(func() { close(b.started) })
	}
	return len(p), nil
}

// userMessage returns the helper's user-facing failure message, if any.
func (b *markerBuffer) userMessage() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	msg := ""
	for _, line := range strings.Split(b.buf.String(), "\n") {
		if m, ok := strings.CutPrefix(strings.TrimSpace(line), privileged.UserMessagePrefix); ok {
			msg = m
		}
	}
	return msg
}

type session struct {
	conn   *websocket.Conn
	ptmx   *os.File
	cancel context.CancelFunc

	endOnce sync.Once
	reason  string

	lastInput atomic.Int64 // unix nanoseconds
	warned    atomic.Bool

	ctl chan []byte
}

func (s *session) end(reason string) {
	s.endOnce.Do(func() {
		s.reason = reason
		s.cancel()
	})
}

// control queues a control message; it is dropped when the client is not
// keeping up, which bounds memory.
func (s *session) control(m serverMessage) {
	select {
	case s.ctl <- encode(m):
	default:
	}
}

// pollableMaster replaces the PTY master by a descriptor in non-blocking
// mode that is registered with the runtime poller. creack/pty leaves the
// master in blocking mode (it calls File.Fd), and closing such a file does
// not take effect while a Read is blocked on it: the terminal would not be
// hung up until the shell printed something. The panel cannot signal the
// helper (it runs as root), so closing the master must work.
func pollableMaster(f *os.File) (*os.File, error) {
	fd, err := unix.FcntlInt(f.Fd(), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return nil, err
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	nf := os.NewFile(uintptr(fd), f.Name())
	_ = f.Close()
	return nf, nil
}

// setSize sets the terminal size without putting the master back into
// blocking mode, which pty.Setsize would do.
func setSize(f *os.File, cols, rows int) error {
	sc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var ioErr error
	err = sc.Control(func(fd uintptr) {
		ioErr = unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ,
			&unix.Winsize{Col: uint16(cols), Row: uint16(rows)})
	})
	if err != nil {
		return err
	}
	return ioErr
}

func parseSize(r *http.Request) (cols, rows int) {
	cols, _ = strconv.Atoi(r.URL.Query().Get("cols"))
	rows, _ = strconv.Atoi(r.URL.Query().Get("rows"))
	if !termcheck.ValidSize(cols, rows) {
		return 80, 24
	}
	return cols, rows
}

// refuse tells the client why no terminal was opened and closes the socket.
func refuse(conn *websocket.Conn, reason, message string) {
	_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
	_ = conn.WriteMessage(websocket.TextMessage, encode(serverMessage{Type: "error", Reason: reason, Message: message}))
	_ = conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.ClosePolicyViolation, reason), time.Now().Add(writeWait))
	_ = conn.Close()
}

func (m *Module) handleSocket(w http.ResponseWriter, r *http.Request) {
	sess := auth.From(r.Context())
	if sess == nil {
		httpx.Fail(w, r, httpx.Unauthorized())
		return
	}
	actor := sess.Actor()
	if sess.User.Role != auth.RoleAdmin {
		m.deps.Audit.Log(r.Context(), actor, "terminal.open", "", "yetkisiz erişim denemesi", false)
		httpx.Fail(w, r, httpx.Forbidden())
		return
	}
	cols, rows := parseSize(r)

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // the upgrader has already answered
	}
	conn.SetReadLimit(readLimit)

	if !m.deps.Settings.Bool(settings.KeyTerminalEnabled) {
		refuse(conn, endDisabled, "Terminal erişimi yönetici tarafından kapatıldı.")
		return
	}
	sysUser := strings.TrimSpace(m.deps.Settings.Get(settings.KeyTerminalUser))
	if sysUser == "" {
		refuse(conn, "no_user", "Terminal için sistem kullanıcısı seçilmemiş. Ayarlar bölümünden bir kullanıcı seçin.")
		return
	}
	if _, err := lookupUser(sysUser); err != nil {
		refuse(conn, "invalid_user", userMessage(err))
		return
	}
	if !m.deps.Auth.SessionActive(r.Context(), sess) {
		refuse(conn, endSession, "Panel oturumunuz sona erdi.")
		return
	}
	if ok, msg := m.acquire(sess.User.ID); !ok {
		refuse(conn, "session_limit", msg)
		return
	}
	defer m.release(sess.User.ID)
	m.wg.Add(1)
	defer m.wg.Done()

	// The audit context must outlive the request.
	bg := context.Background()

	// The command is fixed: the helper action and the configured user.
	// Nothing sent by the client takes part in it.
	cmd, err := m.deps.Priv.Command(bg, "terminal-shell", sysUser)
	if err != nil {
		m.deps.Audit.Log(bg, actor, "terminal.open", sysUser, "başlatılamadı", false)
		slog.Error("terminal komutu hazırlanamadı", "error", err.Error())
		refuse(conn, "spawn_failed", "Terminal başlatılamadı.")
		return
	}
	stderr := &markerBuffer{started: make(chan struct{})}
	cmd.Stderr = stderr
	// StartWithSize makes the child a session leader with the PTY as its
	// controlling terminal, and uses the PTY for stdin and stdout.
	master, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		m.deps.Audit.Log(bg, actor, "terminal.open", sysUser, "başlatılamadı", false)
		slog.Error("terminal başlatılamadı", "error", err.Error())
		refuse(conn, "spawn_failed", "Terminal başlatılamadı.")
		return
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	ptmx, err := pollableMaster(master)
	if err != nil {
		// Closing the master is reliable here: nothing reads from it yet.
		_ = master.Close()
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGHUP)
		select {
		case <-waitCh:
		case <-time.After(killGrace):
			slog.Error("terminal süreci sonlandırılamadı", "pid", cmd.Process.Pid)
		}
		m.deps.Audit.Log(bg, actor, "terminal.open", sysUser, "başlatılamadı", false)
		slog.Error("terminal aygıtı hazırlanamadı", "error", err.Error())
		refuse(conn, "spawn_failed", "Terminal başlatılamadı.")
		return
	}

	started := time.Now()
	exited := false
	var waitErr error

	// reap ends the process tree and collects the child. It always closes
	// the PTY.
	reap := func() {
		if !exited {
			pid := cmd.Process.Pid
			// The child is root (sudo), so these signals are normally
			// refused; closing the PTY is what reliably hangs the
			// session up, and the helper then ends the process tree.
			_ = syscall.Kill(-pid, syscall.SIGHUP)
			_ = ptmx.Close()
			select {
			case waitErr = <-waitCh:
				exited = true
			case <-time.After(killGrace):
				_ = syscall.Kill(-pid, syscall.SIGKILL)
				_ = cmd.Process.Kill()
				select {
				case waitErr = <-waitCh:
					exited = true
				case <-time.After(reapWait):
					slog.Error("terminal süreci sonlandırılamadı", "pid", pid)
				}
			}
		}
		_ = ptmx.Close()
	}

	select {
	case <-stderr.started:
	case waitErr = <-waitCh:
		exited = true
		// The marker may be read after the process is reaped.
		select {
		case <-stderr.started:
		default:
			reap()
			msg := stderr.userMessage()
			if msg == "" {
				msg = "Terminal başlatılamadı. Yardımcı servis çalıştırılamıyor."
			}
			m.deps.Audit.Log(bg, actor, "terminal.open", sysUser, msg, false)
			slog.Warn("terminal başlatılamadı", "user", sysUser, "reason", msg)
			refuse(conn, "spawn_failed", msg)
			return
		}
	case <-time.After(startWait):
		reap()
		m.deps.Audit.Log(bg, actor, "terminal.open", sysUser, "zaman aşımı", false)
		refuse(conn, "spawn_failed", "Terminal zamanında başlatılamadı.")
		return
	case <-m.base.Done():
		reap()
		refuse(conn, endShutdown, "Panel kapatılıyor.")
		return
	}
	m.deps.Audit.Log(bg, actor, "terminal.open", sysUser, "", true)

	ctx, cancel := context.WithCancel(m.base)
	defer cancel()
	s := &session{conn: conn, ptmx: ptmx, cancel: cancel, ctl: make(chan []byte, 16)}
	s.lastInput.Store(m.clk.now().UnixNano())

	out := make(chan []byte)
	ack := make(chan struct{})
	outDone := make(chan struct{})
	writerDone := make(chan struct{})
	readerDone := make(chan struct{})

	// PTY -> writer. One chunk is in flight at a time: the next read
	// happens only after the previous chunk reached the client, so a
	// process that prints quickly is slowed down by the kernel's PTY
	// buffer instead of growing the panel's memory.
	go func() {
		defer close(outDone)
		buf := make([]byte, outputChunk)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				select {
				case out <- buf[:n]:
				case <-ctx.Done():
					return
				}
				select {
				case <-ack:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// Writer: the only goroutine that writes to the socket while the
	// session runs.
	go func() {
		defer close(writerDone)
		ping := time.NewTicker(pingEvery)
		defer ping.Stop()
		for {
			var err error
			select {
			case <-ctx.Done():
				return
			case b := <-out:
				_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
				err = conn.WriteMessage(websocket.BinaryMessage, b)
				if err == nil {
					select {
					case ack <- struct{}{}:
					case <-ctx.Done():
						return
					}
				}
			case b := <-s.ctl:
				_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
				err = conn.WriteMessage(websocket.TextMessage, b)
			case <-ping.C:
				err = conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeWait))
			}
			if err != nil {
				s.end(endSlow)
				return
			}
		}
	}()

	// Socket -> PTY.
	go func() {
		defer close(readerDone)
		_ = conn.SetReadDeadline(time.Now().Add(pongWait))
		conn.SetPongHandler(func(string) error {
			return conn.SetReadDeadline(time.Now().Add(pongWait))
		})
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				s.end(endClient)
				return
			}
			_ = conn.SetReadDeadline(time.Now().Add(pongWait))
			switch mt {
			case websocket.BinaryMessage:
				if len(data) == 0 {
					continue
				}
				s.lastInput.Store(m.clk.now().UnixNano())
				s.warned.Store(false)
				if _, err := ptmx.Write(data); err != nil {
					s.end(endIO)
					return
				}
			case websocket.TextMessage:
				var msg clientMessage
				if json.Unmarshal(data, &msg) != nil {
					continue
				}
				switch msg.Type {
				case "resize":
					if termcheck.ValidSize(msg.Cols, msg.Rows) {
						// Setting the size makes the kernel send
						// SIGWINCH to the terminal's foreground
						// process group.
						_ = setSize(ptmx, msg.Cols, msg.Rows)
					}
				case "ping":
					s.control(serverMessage{Type: "pong"})
				}
			}
		}
	}()

	// Supervisor.
	tickC, stopTick := m.clk.ticker(time.Second)
	defer stopTick()
	deadlineC, stopDeadline := m.clk.timer(maxSessionLength)
	defer stopDeadline()
	idleAtEnd := m.idleTimeout()
	authFailures := 0
	seconds := 0
loop:
	for {
		select {
		case waitErr = <-waitCh:
			exited = true
			// Let the last output reach the client.
			select {
			case <-outDone:
			case <-ctx.Done():
			case <-time.After(flushWait):
			}
			s.end(endExit)
			break loop
		case <-ctx.Done():
			break loop
		case <-deadlineC:
			s.end(endMaxLength)
			break loop
		case <-tickC:
			seconds++
			timeout := m.idleTimeout()
			idle := m.clk.now().Sub(time.Unix(0, s.lastInput.Load()))
			if idle >= timeout {
				idleAtEnd = timeout
				s.end(endIdle)
				break loop
			}
			lead := idleWarningLead
			if timeout/2 < lead {
				lead = timeout / 2
			}
			if left := timeout - idle; left <= lead && !s.warned.Load() {
				s.warned.Store(true)
				sec := int(left / time.Second)
				s.control(serverMessage{Type: "idle_warning", Seconds: &sec})
			}
			if seconds%int(settingsCheckEvery/time.Second) == 0 {
				if !m.deps.Settings.Bool(settings.KeyTerminalEnabled) {
					s.end(endDisabled)
					break loop
				}
				if strings.TrimSpace(m.deps.Settings.Get(settings.KeyTerminalUser)) != sysUser {
					s.end(endUserChanged)
					break loop
				}
			}
			// A failed check is repeated once a few seconds later so a
			// momentary database error does not end the terminal.
			if seconds%int(authCheckEvery/time.Second) == 0 || (authFailures > 0 && seconds%3 == 0) {
				if m.deps.Auth.SessionActive(ctx, sess) {
					authFailures = 0
				} else if authFailures++; authFailures >= 2 {
					s.end(endSession)
					break loop
				}
			}
		}
	}
	s.end(endShutdown) // no effect when a reason is already set
	reason := s.reason

	// The writer stops on cancellation (at most one write deadline later);
	// after that this goroutine is the only writer.
	select {
	case <-writerDone:
	case <-time.After(writeWait + time.Second):
	}
	code := exitCode(waitErr)
	if reason != endClient && reason != endSlow {
		final := serverMessage{Type: "error", Reason: reason, Message: endMessage(reason, idleAtEnd)}
		closeCode := websocket.ClosePolicyViolation
		switch reason {
		case endExit:
			final = serverMessage{Type: "exit", Code: &code}
			closeCode = websocket.CloseNormalClosure
		case endShutdown:
			closeCode = websocket.CloseGoingAway
		}
		_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
		_ = conn.WriteMessage(websocket.TextMessage, encode(final))
		_ = conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(closeCode, reason), time.Now().Add(writeWait))
	}

	reap()
	_ = conn.Close()
	<-readerDone
	<-outDone

	detail := "süre=" + strconv.Itoa(int(time.Since(started)/time.Second)) + "s neden=" + reason
	if exited {
		detail += " çıkış_kodu=" + strconv.Itoa(exitCode(waitErr))
	} else {
		detail += " süreç_sonlandırılamadı"
	}
	m.deps.Audit.Log(bg, actor, "terminal.close", sysUser, detail, exited)
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

func endMessage(reason string, idle time.Duration) string {
	switch reason {
	case endDisabled:
		return "Terminal erişimi yönetici tarafından kapatıldı."
	case endUserChanged:
		return "Terminalin sistem kullanıcısı değiştirildi. Yeniden bağlanın."
	case endSession:
		return "Panel oturumunuz sona erdiği için terminal kapatıldı."
	case endIdle:
		return "Terminal, " + strconv.Itoa(int(idle/time.Minute)) + " dakika boyunca işlem yapılmadığı için kapatıldı."
	case endMaxLength:
		return "Terminal en uzun oturum süresine (" + strconv.Itoa(int(maxSessionLength/time.Hour)) + " saat) ulaştığı için kapatıldı."
	case endShutdown:
		return "Panel kapatıldığı için terminal sonlandırıldı."
	case endIO:
		return "Terminal ile bağlantı kesildi."
	}
	return "Terminal kapatıldı."
}
