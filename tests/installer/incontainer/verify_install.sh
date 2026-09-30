#!/usr/bin/env bash
# Kurulumdan sonra kurulum betiğinin her iddiasını kapsayıcıyı inceleyerek doğrular.
# Kullanım: verify_install.sh <kimlik öneki> <port>
set -uo pipefail
. /opt/tests/lib.sh
P=${1:?önek}; PORT=${2:-8080}

# --- kullanıcı ve grup ---
IFS=: read -r _ _ uid _ _ home shell < <(getent passwd myserver) || true
expect_eq "$P.user.home"  "myserver kullanıcısının ev dizini" "/var/lib/myserver" "${home:-}"
expect_eq "$P.user.shell" "myserver kullanıcısı oturum açamaz" "/usr/sbin/nologin" "${shell:-}"
if [[ -n "${uid:-}" ]] && (( uid > 0 && uid < 1000 )); then pass "$P.user.system" "myserver bir sistem kullanıcısı (uid $uid)"
else fail "$P.user.system" "myserver bir sistem kullanıcısı" "uid=${uid:-yok}"; fi
expect_ok "$P.group" "myserver grubu var" getent group myserver
expect_match "$P.group.docker" "myserver docker grubunun üyesi" '(^| )docker( |$)' "$(id -nG myserver 2>&1)"
expect_eq "$P.user.count" "passwd içinde tek myserver kaydı" "1" "$(grep -c '^myserver:' /etc/passwd)"

# --- dizinler ---
expect_stat "$P.dir.data"    /var/lib/myserver         myserver:myserver 750
expect_stat "$P.dir.backups" /var/lib/myserver/backups myserver:myserver 750
expect_stat "$P.dir.panelbk" /var/lib/myserver/backups/panel myserver:myserver 750
expect_stat "$P.dir.apps"    /var/lib/myserver/apps    myserver:myserver 750
expect_stat "$P.dir.tmp"     /var/lib/myserver/tmp     myserver:myserver 750
expect_stat "$P.db"          /var/lib/myserver/myserver.db myserver:myserver 600
expect_stat "$P.dir.conf"    /etc/myserver             root:myserver 750
expect_stat "$P.env.stat"    /etc/myserver/myserver.env root:myserver 640
expect_stat "$P.dir.share"   /usr/local/share/myserver root:root 755
expect_stat "$P.dir.rollback" /usr/local/share/myserver/rollback root:root 700
expect_ok   "$P.manifests" "uygulama tanımları kuruldu" test -f /usr/local/share/myserver/apps/manifests/jellyfin.yaml
for s in install.sh update.sh uninstall.sh backup.sh; do
  expect_stat "$P.script.$s" "/usr/local/share/myserver/scripts/$s" root:root 755
done
expect_eq "$P.workdir" "geçici kurulum dizini kalmadı" "" "$(ls -d /var/lib/myserver-install.* 2>/dev/null)"

# --- programlar ---
expect_stat "$P.bin"    /usr/local/bin/myserver            root:root 755
expect_stat "$P.helper" /usr/local/libexec/myserver-helper root:root 755
if [[ -u /usr/local/libexec/myserver-helper || -g /usr/local/libexec/myserver-helper ]]; then
  fail "$P.helper.nosuid" "yardımcı setuid/setgid değil" "$(stat -c %A /usr/local/libexec/myserver-helper)"
else pass "$P.helper.nosuid" "yardımcı setuid/setgid değil"; fi
expect_eq "$P.nocaps" "programlarda dosya yeteneği (file capability) yok" "" \
  "$(getcap /usr/local/bin/myserver /usr/local/libexec/myserver-helper 2>/dev/null)"

# --- sudoers ---
expect_ok   "$P.visudo" "visudo -c geçiyor" visudo -c
expect_stat "$P.sudoers.stat" /etc/sudoers.d/myserver root:root 440
expect_eq "$P.sudoers.files" "sudoers.d içinde tek myserver dosyası (geçici dosya kalmadı)" "myserver" \
  "$(ls -A /etc/sudoers.d | grep -i myserver | tr '\n' ' ' | sed 's/ $//')"
allowed=$(sudo -l -U myserver 2>&1 | sed -n '/may run the following/,$p' | tail -n +2 | sed 's/^[[:space:]]*//' | grep .)
expect_eq "$P.sudoers.only" "sudo yalnızca yardımcıya izin veriyor" \
  "(root) NOPASSWD: /usr/local/libexec/myserver-helper" "$allowed"
expect_fail "$P.sudoers.deny" "myserver sudo ile başka komut çalıştıramıyor" runuser -u myserver -- sudo -n /usr/bin/id
expect_eq "$P.ping" "sudo -u myserver sudo -n yardımcı ping -> pong" "pong" \
  "$(sudo -u myserver sudo -n /usr/local/libexec/myserver-helper ping 2>&1)"

# --- servis ---
expect_stat "$P.unit.stat" /etc/systemd/system/myserver.service root:root 644
expect_eq "$P.unit.enabled" "servis etkin (enabled)" "enabled" "$(systemctl is-enabled myserver 2>&1)"
wait_http "$PORT" 30
expect_eq "$P.unit.active"  "servis çalışıyor (active)" "active" "$(systemctl is-active myserver 2>&1)"
expect_eq "$P.unit.verify" "systemd-analyze verify uyarısız" "" "$(systemd-analyze verify /etc/systemd/system/myserver.service 2>&1 | grep -i myserver.service || true)"

# --- ortam dosyası ---
expect_eq "$P.env.listen" "ortam dosyasında seçilen port, tek satır" ":${PORT}" \
  "$(sed -n 's/^MYSERVER_LISTEN=//p' /etc/myserver/myserver.env)"
dups=$(grep -E '^[A-Z_]+=' /etc/myserver/myserver.env | cut -d= -f1 | sort | uniq -d | tr '\n' ' ')
expect_eq "$P.env.nodup" "ortam dosyasında yinelenen anahtar yok" "" "$dups"

# --- HTTP ---
body=$(api_status "$PORT")
expect_match "$P.http.envelope" "GET /api/v1/auth/status JSON zarfını döndürüyor" \
  '^\{"success":true,"data":\{.*\},"error":null\}$' "$body"
expect_match "$P.http.index" "kök adres gömülü arayüzü sunuyor" '<!doctype html|<!DOCTYPE html' \
  "$(curl -s --max-time 5 "http://127.0.0.1:${PORT}/" | head -c 300)"

# --- süreç kimliği ve yetenekler ---
pid=$(systemctl show -p MainPID --value myserver)
st="/proc/$pid/status"
expect_eq "$P.proc.user" "panel süreci myserver kullanıcısıyla çalışıyor (root değil)" "myserver" "$(ps -o user= -p "$pid" | tr -d ' ')"
expect_eq "$P.proc.uid" "gerçek/etkin/saklı/fs uid hepsi myserver" "$uid $uid $uid $uid" \
  "$(awk '$1=="Uid:"{print $2,$3,$4,$5}' "$st")"
want_caps="cap_chown,cap_dac_override,cap_dac_read_search,cap_fowner"
for k in CapEff CapPrm CapAmb CapInh; do
  hex=$(awk -v k="$k:" '$1==k{print $2}' "$st")
  expect_eq "$P.proc.$k" "$k tam olarak dört yetenek" "$want_caps" "$(capsh --decode="$hex" | cut -d= -f2)"
done
bnd_init=$(awk '$1=="CapBnd:"{print $2}' /proc/1/status)
expect_eq "$P.proc.CapBnd" "CapBnd sınırlanmamış (PID 1 ile aynı)" "$bnd_init" "$(awk '$1=="CapBnd:"{print $2}' "$st")"
expect_eq "$P.proc.nnp" "NoNewPrivs kapalı (sudo çalışabilsin)" "0" "$(awk '$1=="NoNewPrivs:"{print $2}' "$st")"

# Yardımcı: servisle aynı özelliklere sahip geçici bir birimden sudo ile başlatılır
# ve /proc üzerinden incelenir (uzun süren "services-logs-follow" eylemi kullanılır).
systemctl stop mstest-capprobe >/dev/null 2>&1; systemctl reset-failed mstest-capprobe >/dev/null 2>&1
systemd-run --quiet --unit=mstest-capprobe -p User=myserver -p Group=myserver -p SupplementaryGroups=docker \
  -p "AmbientCapabilities=CAP_DAC_OVERRIDE CAP_DAC_READ_SEARCH CAP_CHOWN CAP_FOWNER" -p KeyringMode=private \
  /usr/bin/sudo -n /usr/local/libexec/myserver-helper services-logs-follow myserver.service
hp=""
for _ in 1 2 3 4 5 6 7 8 9 10; do
  hp=$(pgrep -x -u 0 myserver-helper | head -n 1)
  [[ -n "$hp" ]] && break
  sleep 0.5
done
if [[ -n "$hp" ]]; then
  expect_eq "$P.helper.uid" "sudo ile başlatılan yardımcı uid 0" "0 0 0 0" "$(awk '$1=="Uid:"{print $2,$3,$4,$5}' "/proc/$hp/status")"
  expect_eq "$P.helper.caps" "yardımcı tam root yeteneklerine sahip (CapEff = tam küme)" "$bnd_init" \
    "$(awk '$1=="CapEff:"{print $2}' "/proc/$hp/status")"
else
  fail "$P.helper.caps" "yardımcı süreci incelenemedi" "$(journalctl -u mstest-capprobe -n 5 --no-pager 2>&1)"
fi
systemctl stop mstest-capprobe >/dev/null 2>&1

# --- yetkisiz kullanıcının salt okunur systemctl sorguları (güncelleme modülü) ---
out=$(runuser -u myserver -- /usr/bin/systemctl is-active myserver-apt-upgrade 2>&1); rc=$?
expect_match "$P.unpriv.isactive" "myserver kullanıcısı 'systemctl is-active' çalıştırabiliyor (rc=$rc, çıktı=$out)" \
  '^(inactive|active|failed|activating|deactivating)$' "$out"
out=$(runuser -u myserver -- /usr/bin/systemctl show -p LoadState,ActiveState,Result,ExecMainStatus,ExecMainCode myserver-apt-upgrade 2>&1); rc=$?
if (( rc == 0 )) && grep -q '^LoadState=' <<<"$out" && grep -q '^ActiveState=' <<<"$out" \
   && grep -q '^Result=' <<<"$out" && grep -q '^ExecMainStatus=' <<<"$out" && grep -q '^ExecMainCode=' <<<"$out"; then
  pass "$P.unpriv.show" "myserver kullanıcısı 'systemctl show -p ...' çalıştırabiliyor (beş özellik de döndü)"
else
  fail "$P.unpriv.show" "myserver kullanıcısı 'systemctl show -p ...' çalıştırabiliyor" "rc=$rc $out"
fi
