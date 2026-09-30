#!/usr/bin/env bash
# Senaryo 9 (düzey 1 ve 2): uninstall.sh. Kurulu panel ve tamamlanmış sihirbaz gerektirir.
set -uo pipefail
. /opt/tests/lib.sh
P=s09
UN=/usr/local/share/myserver/scripts/uninstall.sh
cp "$UN" /root/uninstall.sh   # betik kendini de sildiği için kopyası kullanılır

# Etkileşimli çalıştırma: betik /dev/tty'den okur; "script" sahte bir terminal sağlar.
interactive() { # <girdi satırları...>; çıktı: OUT, RC
  local input
  input=$(printf '%s\n' "$@")
  OUT=$( { sleep 1; while IFS= read -r l; do printf '%s\n' "$l"; sleep 1; done <<<"$input"; sleep 1; } \
        | script -qec "bash /root/uninstall.sh" /dev/null 2>&1 ); RC=$?
}
gone() { # <kimlik>
  local id=$1
  expect_nothing_installed "$id.files"
  expect_fail "$id.share" "/usr/local/share/myserver kaldırıldı" test -e /usr/local/share/myserver
  expect_fail "$id.user" "myserver kullanıcısı kaldırıldı" getent passwd myserver
  expect_fail "$id.grp" "myserver grubu kaldırıldı" getent group myserver
  expect_ok   "$id.visudo" "visudo -c hâlâ geçiyor" visudo -c
  expect_eq   "$id.unit" "systemd birimi artık tanımlı değil" "not-found" "$(systemctl show -p LoadState --value myserver.service)"
  expect_eq   "$id.enabled" "etkinleştirme bağlantısı kalmadı" "" "$(find /etc/systemd/system -name 'myserver.service' 2>/dev/null)"
  expect_eq   "$id.procs" "myserver süreci kalmadı" "" "$(pgrep -x myserver || true)"
  expect_fail "$id.updates" "/var/lib/myserver-updates kaldırıldı" test -e /var/lib/myserver-updates
  expect_fail "$id.lock" "güncelleme kilidi kaldırıldı" test -e /run/myserver-update.lock
  expect_fail "$id.port" "8080 portu artık dinlenmiyor" curl -s -o /dev/null --max-time 2 http://127.0.0.1:8080/
  expect_eq   "$id.dockergrp" "docker grubunda myserver üyeliği kalmadı" "0" "$(getent group docker | tr ':,' '\n\n' | grep -cx myserver)"
}
intact() { # <kimlik>
  expect_eq "$1.svc" "servis çalışıyor" "active" "$(systemctl is-active myserver)"
  expect_ok "$1.bin" "program yerinde" test -x /usr/local/bin/myserver
  expect_ok "$1.sudoers" "sudoers yerinde" test -f /etc/sudoers.d/myserver
  expect_ok "$1.db" "veritabanı yerinde" test -s /var/lib/myserver/myserver.db
  expect_ok "$1.env" "ortam dosyası yerinde" test -s /etc/myserver/myserver.env
  expect_ok "$1.user" "kullanıcı yerinde" getent passwd myserver
}

# Yardımcının ilk kullanımda oluşturduğu dizini ve güncelleme kilidini benzet.
install -d -m 0755 -o root -g root /var/lib/myserver-updates
echo log >/var/lib/myserver-updates/apt-upgrade.log
: >/run/myserver-update.lock
echo "kaldirma-oncesi" >/var/lib/myserver/MARKER
bash /usr/local/share/myserver/scripts/backup.sh >/dev/null 2>&1
backups_before=$(ls -1 /var/lib/myserver/backups/panel | wc -l | tr -d ' ')
sudoers_other_before=$(ls -A /etc/sudoers.d | grep -v '^myserver$' | sort | tr '\n' ' ')

# --- bağımsız değişken denetimleri (hiçbir şey değişmemeli) ---
chk_refuse() { # <kimlik> <açıklama> <desen> <seçenekler...>
  local id=$1 d=$2 re=$3 out rc; shift 3
  out=$(bash /root/uninstall.sh "$@" 2>&1); rc=$?
  if (( rc != 0 )) && grep -Eq -- "$re" <<<"$out"; then pass "$id" "$d"; else fail "$id" "$d" "rc=$rc $(tail -n 4 <<<"$out")"; fi
}
chk_refuse "$P.arg.yes.nolevel" "--yes, --level olmadan reddediliyor" 'level de verilmelidir' --yes
chk_refuse "$P.arg.l2.noconfirm" "--yes --level 2, --confirm-data-loss olmadan reddediliyor" 'confirm-data-loss' --yes --level 2
chk_refuse "$P.arg.l3.noconfirm" "--yes --level 3, --confirm-data-loss olmadan reddediliyor" 'confirm-data-loss' --yes --level 3
chk_refuse "$P.arg.vol.l2" "--remove-volumes düzey 2 ile reddediliyor" 'yalnızca --level 3' --yes --level 2 --confirm-data-loss --remove-volumes
for bad in 0 4 12 abc '1;id' ''; do
  chk_refuse "$P.arg.level[$bad]" "geçersiz düzey reddediliyor: '$bad'" 'Düzey geçersiz|level de verilmelidir' --yes --level "$bad"
done
chk_refuse "$P.arg.unknown" "bilinmeyen seçenek reddediliyor" 'Bilinmeyen seçenek' --hepsini-sil
OUT=$(runuser -u nobody -- bash /usr/local/share/myserver/scripts/uninstall.sh --level 1 --yes 2>&1); RC=$?
if (( RC != 0 )) && grep -q 'root yetkisiyle' <<<"$OUT"; then pass "$P.nonroot" "root olmayan kullanıcıyla reddediliyor"
else fail "$P.nonroot" "root olmayan kullanıcıyla reddediliyor" "rc=$RC $OUT"; fi
OUT=$(bash /root/uninstall.sh </dev/null 2>&1 | cat); RC=${PIPESTATUS[0]}
intact "$P.args.intact"

# --- etkileşimli: vazgeçme ve yanlış onay ---
interactive 0
expect_match "$P.i.cancel" "menüde 0: vazgeçildi" 'Vazgeçildi' "$OUT"
intact "$P.i.cancel"
interactive 1 h
expect_match "$P.i.l1.no" "düzey 1, 'h' yanıtı: vazgeçildi" 'Vazgeçildi' "$OUT"
intact "$P.i.l1.no"
for wrong in kaldir KALDIR! evet ''; do
  interactive 2 "$wrong"
  expect_match "$P.i.l2.wrong[$wrong]" "düzey 2, yanlış onay '$wrong': hiçbir şey silinmedi" 'Onay verilmedi' "$OUT"
  intact "$P.i.l2.wrong[$wrong]"
done
expect_eq "$P.i.marker" "yanlış onaylardan sonra veriler duruyor" "kaldirma-oncesi" "$(cat /var/lib/myserver/MARKER)"

# --- düzey 1 ---
OUT=$(bash /root/uninstall.sh --level 1 --yes 2>&1); RC=$?
expect_eq "$P.l1.rc" "düzey 1 çıkış kodu 0" "0" "$RC"
gone "$P.l1"
expect_eq "$P.l1.marker" "düzey 1: /var/lib/myserver korundu" "kaldirma-oncesi" "$(cat /var/lib/myserver/MARKER 2>&1)"
expect_ok "$P.l1.db" "düzey 1: veritabanı korundu" test -s /var/lib/myserver/myserver.db
expect_eq "$P.l1.backups" "düzey 1: yedekler korundu" "$backups_before" "$(ls -1 /var/lib/myserver/backups/panel | wc -l | tr -d ' ')"
expect_ok "$P.l1.env" "düzey 1: /etc/myserver/myserver.env korundu" test -s /etc/myserver/myserver.env
expect_eq "$P.l1.sudoersd" "diğer sudoers dosyalarına dokunulmadı" "$sudoers_other_before" "$(ls -A /etc/sudoers.d | sort | tr '\n' ' ')"
expect_match "$P.l1.msg" "korunan dizinler bildirildi" 'Korunan dizinler' "$OUT"
OUT=$(bash /root/uninstall.sh --level 1 --yes 2>&1); RC=$?
expect_eq "$P.l1.again" "zaten kaldırılmış sistemde yeniden çalıştırma başarılı" "0" "$RC"

# Araya başka bir sistem kullanıcısı girsin: yeniden kurulumda uid değişebilir.
useradd --system --no-create-home --shell /usr/sbin/nologin mstest-filler 2>/dev/null
fresh_release v1.0.0
OUT=$(bash "$INSTALLER_DIR/scripts/install.sh" --yes --no-docker 2>&1); RC=$?
expect_eq "$P.reinstall.rc" "düzey 1'den sonra yeniden kurulum başarılı" "0" "$RC"
expect_match "$P.reinstall.login" "yeniden kurulumda eski veriler kullanılıyor (yönetici oturum açabiliyor)" '"authenticated":true' "$(api_login 8080)"
expect_eq "$P.reinstall.owner" "veri dizininde eski kullanıcı numarasına ait dosya kalmadı (apps/ hariç)" "" \
  "$(find /var/lib/myserver -path /var/lib/myserver/apps -prune -o ! -user myserver -print)"
bash /opt/tests/verify_install.sh "$P.reinstall.v" 8080
cp /usr/local/share/myserver/scripts/uninstall.sh /root/uninstall.sh
install -d -m 0755 -o root -g root /var/lib/myserver-updates

# --- düzey 2 (etkileşimli, doğru onay) ---
interactive 2 KALDIR
expect_eq "$P.l2.rc" "düzey 2 (KALDIR yazılarak) çıkış kodu 0" "0" "$RC"
expect_match "$P.l2.warn" "geri alınamaz uyarısı gösterildi" 'GERİ ALINAMAZ' "$OUT"
gone "$P.l2"
expect_fail "$P.l2.data" "düzey 2: /var/lib/myserver silindi" test -e /var/lib/myserver
expect_fail "$P.l2.conf" "düzey 2: /etc/myserver silindi" test -e /etc/myserver
expect_ok "$P.l2.pkgs" "sistem paketleri kaldırılmadı (sudo, ufw, curl duruyor)" bash -c 'command -v sudo && command -v ufw && command -v curl'
expect_ok "$P.l2.filler" "başka kullanıcılara dokunulmadı" getent passwd mstest-filler
userdel mstest-filler 2>/dev/null

# --- düzey 2 (etkileşimsiz) temiz kurulum üzerinde ---
fresh_release v1.0.0
bash "$INSTALLER_DIR/scripts/install.sh" --yes --no-docker >/dev/null 2>&1
expect_match "$P.l2b.fresh" "düzey 2'den sonra kurulum sıfırdan başlıyor (sihirbaz yeniden açık)" '"setup_complete":false' "$(api_status 8080)"
OUT=$(bash /usr/local/share/myserver/scripts/uninstall.sh --level 2 --yes --confirm-data-loss 2>&1); RC=$?
expect_eq "$P.l2b.rc" "düzey 2 (--yes --confirm-data-loss, kurulu yerinden çalıştırılarak) çıkış kodu 0" "0" "$RC"
gone "$P.l2b"
expect_fail "$P.l2b.data" "veriler silindi" test -e /var/lib/myserver
