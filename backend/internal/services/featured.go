package services

import (
	"encoding/json"
	"strings"

	"myserver/internal/httpx"
	"myserver/internal/services/servicescheck"
)

const settingFeatured = "services.featured"

const maxFeatured = 24

var defaultFeatured = []string{
	"docker.service",
	"ssh.service",
	"chrony.service",
	"ufw.service",
	"nginx.service",
	"tailscaled.service",
	"smbd.service",
	"nfs-server.service",
	servicescheck.PanelUnit,
}

// alternatives lists units that provide the same function under different
// names; the first one installed on the host is shown.
var alternatives = [][]string{
	{"ssh.service", "sshd.service"},
	{"chrony.service", "chronyd.service", "systemd-timesyncd.service"},
	{"smbd.service", "smb.service"},
	{"nfs-server.service", "nfs-kernel-server.service"},
}

var friendlyNames = map[string]string{
	"docker.service":              "Docker",
	"containerd.service":          "containerd",
	"ssh.service":                 "SSH",
	"sshd.service":                "SSH",
	"chrony.service":              "Chrony (NTP)",
	"chronyd.service":             "Chrony (NTP)",
	"systemd-timesyncd.service":   "Zaman Eşitleme (NTP)",
	"ufw.service":                 "UFW (Güvenlik Duvarı)",
	"nginx.service":               "Nginx",
	"tailscaled.service":          "Tailscale",
	"smbd.service":                "Samba (SMB)",
	"smb.service":                 "Samba (SMB)",
	"nfs-server.service":          "NFS Sunucusu",
	"nfs-kernel-server.service":   "NFS Sunucusu",
	"systemd-networkd.service":    "Ağ Yöneticisi (networkd)",
	"NetworkManager.service":      "NetworkManager",
	"systemd-resolved.service":    "DNS Çözümleyici",
	"cron.service":                "Cron",
	"fail2ban.service":            "Fail2ban",
	servicescheck.PanelUnit:       "MyServer Panel",
	"unattended-upgrades.service": "Otomatik Güncellemeler",
}

func displayName(unit string) string {
	if n, ok := friendlyNames[unit]; ok {
		return n
	}
	return strings.TrimSuffix(unit, ".service")
}

func candidates(unit string) []string {
	for _, group := range alternatives {
		for _, g := range group {
			if g == unit {
				out := []string{unit}
				for _, o := range group {
					if o != unit {
						out = append(out, o)
					}
				}
				return out
			}
		}
	}
	return []string{unit}
}

// resolveFeatured returns the configured featured services in order. A unit
// that is not installed is reported as such rather than hidden.
func resolveFeatured(configured []string, byUnit map[string]Service) []Service {
	out := make([]Service, 0, len(configured))
	seen := map[string]bool{}
	for _, want := range configured {
		if !servicescheck.ValidUnit(want) {
			continue
		}
		var chosen *Service
		for _, c := range candidates(want) {
			if s, ok := byUnit[c]; ok && s.Installed {
				chosen = &s
				break
			}
		}
		if chosen == nil {
			risk, kind := servicescheck.Classify(want)
			chosen = &Service{
				Unit: want, Name: displayName(want),
				LoadState: "not-found", ActiveState: "inactive", SubState: "dead",
				State: StateNotInstalled, TriggeredBy: []string{},
				Risk: risk, RiskKind: kind,
			}
		}
		if seen[chosen.Unit] {
			continue
		}
		seen[chosen.Unit] = true
		chosen.Featured = true
		out = append(out, *chosen)
	}
	return out
}

// validateFeatured normalizes the services.featured setting.
func validateFeatured(v string) (string, error) {
	var items []string
	if err := json.Unmarshal([]byte(v), &items); err != nil {
		return "", httpx.BadRequest("Öne çıkan servis listesi geçersiz.")
	}
	if len(items) > maxFeatured {
		return "", httpx.BadRequest("En fazla 24 servis öne çıkarılabilir.")
	}
	out := []string{}
	seen := map[string]bool{}
	for _, it := range items {
		it = strings.TrimSpace(it)
		if it == "" {
			continue
		}
		if !strings.HasSuffix(it, ".service") {
			it += ".service"
		}
		if !servicescheck.ValidUnit(it) || servicescheck.IsTemplate(it) {
			return "", httpx.BadRequest("Servis adı geçersiz: " + cleanText(it, 140))
		}
		if !seen[it] {
			seen[it] = true
			out = append(out, it)
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", httpx.Internal(err)
	}
	return string(b), nil
}
