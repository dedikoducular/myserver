#!/usr/bin/env bash
# Senaryo 6: Docker kurulum yolu. Docker'sız TEMİZ bir kapsayıcıda çalışır.
# "Betik doğru olanı yaptı" ile "daemon bu kum havuzunda çalışabildi" ayrı raporlanır.
set -uo pipefail
. /opt/tests/lib.sh
P=s06

expect_eq "$P.pre" "başlangıçta docker kurulu değil" "" "$(command -v docker dockerd || true)"
fresh_release v1.0.0
bash "$INSTALLER_DIR/scripts/install.sh" --yes >/tmp/install-docker.log 2>&1
rc=$?
if (( rc == 0 )); then pass "$P.run" "install.sh --yes (Docker dahil) çıkış kodu 0"
else fail "$P.run" "install.sh --yes (Docker dahil) çıkış kodu 0" "rc=$rc $(tail -n 15 /tmp/install-docker.log)"; fi

# --- betiğin yaptıkları ---
. /etc/os-release
codename=${UBUNTU_CODENAME:-$VERSION_CODENAME}
expect_stat "$P.key" /etc/apt/keyrings/docker.asc root:root 644
expect_match "$P.key.pgp" "imza anahtarı bir PGP açık anahtarı" 'BEGIN PGP PUBLIC KEY BLOCK' "$(head -n 1 /etc/apt/keyrings/docker.asc)"
expect_eq "$P.list" "docker.list: resmi depo, signed-by anahtarlığı, doğru dağıtım ve kod adı" \
  "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/${ID} ${codename} stable" \
  "$(cat /etc/apt/sources.list.d/docker.list)"
expect_eq "$P.list.tmp" "sources.list.d içinde geçici dosya kalmadı" "" "$(ls /etc/apt/sources.list.d | grep 'docker.list\.' || true)"
expect_eq "$P.key.tmp" "keyrings içinde geçici dosya kalmadı" "" "$(ls /etc/apt/keyrings | grep 'docker.asc\.' || true)"
for pkg in docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin; do
  expect_eq "$P.pkg.$pkg" "paket kurulu: $pkg" "install ok installed" "$(dpkg-query -W -f='${Status}' "$pkg" 2>/dev/null)"
done
expect_match "$P.origin" "docker-ce Docker'ın deposundan geldi" 'download\.docker\.com' "$(apt-cache policy docker-ce 2>/dev/null)"
expect_eq "$P.enabled" "docker servisi etkin (enabled)" "enabled" "$(systemctl is-enabled docker 2>&1)"
expect_match "$P.group" "myserver docker grubunun üyesi" '(^| )docker( |$)' "$(id -nG myserver)"
expect_eq "$P.state" "install.state: Docker'ı bu betik kurdu" "1" "$(sed -n 's/^DOCKER_INSTALLED_BY_MYSERVER=//p' /etc/myserver/install.state)"
expect_eq "$P.notcp" "Docker TCP soketi açılmadı (2375/2376 dinlenmiyor)" "" "$(ss -H -ltn 'sport = :2375 or sport = :2376')"
expect_eq "$P.nodaemonjson" "betik daemon.json yazmadı" "" "$(ls /etc/docker/daemon.json 2>/dev/null)"
expect_eq "$P.nohosts" "docker birimine -H tcp eklenmedi" "" "$(systemctl cat docker.service 2>/dev/null | grep -E 'tcp://' || true)"

# --- daemon bu kum havuzunda çalışabildi mi? (betikten bağımsız) ---
state=$(systemctl is-active docker 2>&1)
if [[ "$state" == active ]]; then
  pass "$P.daemon" "docker daemon kapsayıcı içinde çalışıyor"
  expect_eq "$P.sock" "soket root:docker 660" "root:docker 660" "$(stat -c '%U:%G %a' /var/run/docker.sock 2>&1)"
  expect_ok "$P.sock.user" "myserver kullanıcısı sokete ulaşabiliyor (docker version)" \
    runuser -u myserver -- docker version --format '{{.Server.Version}}'
  expect_fail "$P.sock.nobody" "yetkisiz kullanıcı (nobody) sokete ulaşamıyor" \
    runuser -u nobody -- docker version --format '{{.Server.Version}}'
  pid=$(systemctl show -p MainPID --value myserver)
  expect_match "$P.sock.panel" "panel sürecinin ek grupları docker grubunu içeriyor" \
    "(^|[[:space:]])$(getent group docker | cut -d: -f3)([[:space:]]|$)" "$(awk '$1=="Groups:"{$1="";print}' "/proc/$pid/status")"
  info "$P.daemon.info" "depolama sürücüsü" "$(docker info --format '{{.Driver}} / cgroup {{.CgroupVersion}}' 2>&1)"
else
  info "$P.daemon" "docker daemon bu kum havuzunda ÇALIŞMADI (betikten bağımsız)" \
    "durum=$state; $(journalctl -u docker -n 8 --no-pager 2>&1 | tail -n 8)"
fi
bash /opt/tests/verify_install.sh "$P.v" 8080

# --- yeniden çalıştırma: Docker yeniden kurulmaz, depo yinelenmez ---
list_before=$(sha256sum </etc/apt/sources.list.d/docker.list)
bash "$INSTALLER_DIR/scripts/install.sh" --yes >/tmp/install-docker2.log 2>&1
expect_eq "$P.rerun" "yeniden çalıştırma başarılı" "0" "$?"
expect_match "$P.rerun.msg" "Docker zaten kurulu olarak algılandı" 'Docker zaten kurulu' "$(cat /tmp/install-docker2.log)"
expect_eq "$P.rerun.list" "docker.list değişmedi" "$list_before" "$(sha256sum </etc/apt/sources.list.d/docker.list)"
expect_eq "$P.rerun.state" "Docker sahiplik kaydı korundu" "1" "$(sed -n 's/^DOCKER_INSTALLED_BY_MYSERVER=//p' /etc/myserver/install.state)"
