//go:build linux

package auth

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func checkStorage(dataDir string) Check {
	var st unix.Statfs_t
	if err := unix.Statfs(dataDir, &st); err != nil {
		return Check{OK: false, Message: "Veri dizini okunamadı."}
	}
	free := st.Bavail * uint64(st.Bsize)
	total := st.Blocks * uint64(st.Bsize)
	detail := fmt.Sprintf("%.1f GB boş / %.1f GB", float64(free)/(1<<30), float64(total)/(1<<30))
	if free < 2<<30 {
		return Check{OK: false, Message: "Veri diskinde 2 GB'tan az boş alan var.", Detail: detail}
	}
	return Check{OK: true, Message: "Depolama alanı yeterli.", Detail: detail}
}

func checkDocker(ctx context.Context, host string) Check {
	path, ok := strings.CutPrefix(host, "unix://")
	if !ok {
		return Check{OK: false, Message: "Docker bağlantı adresi desteklenmiyor."}
	}
	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", path)
			},
		},
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/version", nil)
	if err != nil {
		return Check{OK: false, Message: "Docker servisine ulaşılamıyor."}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Check{OK: false, Message: "Docker servisine ulaşılamıyor. Docker kurulu ve çalışıyor olmalıdır."}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Check{OK: false, Message: "Docker servisi beklenmeyen bir yanıt verdi."}
	}
	return Check{OK: true, Message: "Docker çalışıyor."}
}
