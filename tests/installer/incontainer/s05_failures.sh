#!/usr/bin/env bash
# Senaryo 5: hata durumları. TEMİZ (kurulum yapılmamış) bir kapsayıcıda çalışır.
set -uo pipefail
. /opt/tests/lib.sh
P=s05
I="$INSTALLER_DIR/scripts/install.sh"

run_installer() { # çıktı: OUT, RC
  OUT=$(bash "$@" 2>&1); RC=$?
}
refused() { # <kimlik> <açıklama> <beklenen ileti deseni>
  if (( RC != 0 )) && grep -Eq -- "$3" <<<"$OUT"; then pass "$1" "$2"
  else fail "$1" "$2" "rc=$RC $(tail -n 6 <<<"$OUT")"; fi
}

fresh_release v1.0.0

# --- root olmayan kullanıcı ---
cp -r "$INSTALLER_DIR" /opt/rel-copy && chmod -R a+rX /opt/rel-copy
OUT=$(runuser -u nobody -- bash /opt/rel-copy/scripts/install.sh --yes --no-docker 2>&1); RC=$?
refused "$P.nonroot" "root olmayan kullanıcıyla reddediliyor" 'root yetkisiyle'
expect_nothing_installed "$P.nonroot.clean"

# --- desteklenmeyen işletim sistemi (sahte /etc/os-release) ---
cp -a --remove-destination "$(readlink -f /etc/os-release)" /root/os-release.orig
fake_os() { rm -f /etc/os-release; printf '%s\n' "$@" >/etc/os-release; }
restore_os() { rm -f /etc/os-release; ln -s ../usr/lib/os-release /etc/os-release; }
fake_os 'PRETTY_NAME="Debian GNU/Linux 11 (bullseye)"' 'ID=debian' 'VERSION_ID="11"' 'VERSION_CODENAME=bullseye'
run_installer "$I" --yes --no-docker
refused "$P.os.debian11" "Debian 11 --force olmadan reddediliyor" 'desteklenmiyor'
fake_os 'PRETTY_NAME="Fedora Linux 40"' 'ID=fedora' 'VERSION_ID=40'
run_installer "$I" --yes --no-docker
refused "$P.os.fedora" "Fedora --force olmadan reddediliyor" 'desteklenmiyor'
fake_os 'PRETTY_NAME="Ubuntu 23.10"' 'ID=ubuntu' 'VERSION_ID="23.10"' 'VERSION_CODENAME=mantic'
run_installer "$I" --yes --no-docker
refused "$P.os.ubuntu2310" "LTS olmayan Ubuntu 23.10 reddediliyor" 'desteklenmiyor'
expect_nothing_installed "$P.os.clean"
expect_fail "$P.os.nouser" "reddedilen kurulum kullanıcı oluşturmadı" getent passwd myserver

# --force: işletim sistemi denetimini geçer (sonraki engel: dolu port).
systemd-run --quiet --unit=mstest-blocker /usr/bin/python3 -m http.server 8080 --bind 0.0.0.0
sleep 1
fake_os 'PRETTY_NAME="Debian GNU/Linux 11 (bullseye)"' 'ID=debian' 'VERSION_ID="11"' 'VERSION_CODENAME=bullseye'
run_installer "$I" --yes --no-docker --force
if (( RC != 0 )) && grep -q -- '--force verildiği için devam' <<<"$OUT" && grep -q 'portu başka bir program' <<<"$OUT"; then
  pass "$P.os.force" "--force ile işletim sistemi denetimi uyarıyla geçiliyor"
else fail "$P.os.force" "--force ile işletim sistemi denetimi uyarıyla geçiliyor" "rc=$RC $(tail -n 6 <<<"$OUT")"; fi
restore_os
expect_ok "$P.os.restored" "os-release geri yüklendi" grep -q '^ID=' /etc/os-release

# --- port ---
run_installer "$I" --yes --no-docker
refused "$P.port.busy" "kullanımdaki port (8080) reddediliyor" '8080 portu başka bir program'
expect_nothing_installed "$P.port.busy.clean"
expect_fail "$P.port.busy.nouser" "dolu port: kullanıcı oluşturulmadı" getent passwd myserver
systemctl stop mstest-blocker >/dev/null 2>&1
for bad in 80 1023 65536 70000 0 abc -5 '8080;id' '80 80' ''; do
  run_installer "$I" --yes --no-docker --port "$bad"
  refused "$P.port.bad[$bad]" "geçersiz port reddediliyor: '$bad'" 'Port (geçersiz|1024 ile 65535)'
done
run_installer "$I" --yes --no-docker --port
refused "$P.port.missing" "--port değersiz reddediliyor" 'bir değer gerektirir'
run_installer "$I" --yes --bilinmeyen
refused "$P.arg.unknown" "bilinmeyen seçenek reddediliyor" 'Bilinmeyen seçenek'
for bad in 'http://localhost/x' 'ftp://x/y' 'https://x/$(id)' 'https://x/a b' '-o/etc/passwd' 'https://x/;id'; do
  run_installer "$I" --yes --no-docker --release-url "$bad"
  refused "$P.url.bad[$bad]" "geçersiz yayın adresi reddediliyor: '$bad'" 'Yayın adresi geçersiz'
done
for bad in 'v1' '1.2' 'latest;id' '../v1.0.0' '-v1.0.0' 'v1.0.0 x'; do
  run_installer "$I" --yes --no-docker --version "$bad"
  refused "$P.ver.bad[$bad]" "geçersiz sürüm reddediliyor: '$bad'" 'Sürüm geçersiz'
done
expect_nothing_installed "$P.args.clean"

# --- eksik / bozuk dosyalar ---
fresh_release v1.0.0
rm -f "$INSTALLER_DIR/myserver-helper-linux-amd64"
run_installer "$I" --yes --no-docker
refused "$P.bin.missing" "yardımcı dosyası eksikken açıklayıcı iletiyle duruyor" 'Kurulacak dosyalar bulunamadı'
expect_nothing_installed "$P.bin.missing.clean"

fresh_release v1.0.0
head -c 100000 "$INSTALLER_DIR/myserver-linux-amd64" >/root/trunc && cp /root/trunc "$INSTALLER_DIR/myserver-linux-amd64"
run_installer "$I" --yes --no-docker
refused "$P.bin.corrupt.sum" "bozuk program SHA256SUMS ile yakalanıyor" 'SHA-256 doğrulaması başarısız'
expect_nothing_installed "$P.bin.corrupt.sum.clean"

rm -f "$INSTALLER_DIR/SHA256SUMS"
run_installer "$I" --yes --no-docker
refused "$P.bin.corrupt.probe" "SHA256SUMS yokken bozuk program çalıştırma sınamasıyla yakalanıyor" 'çalıştırılamadı'
expect_nothing_installed "$P.bin.corrupt.probe.clean"
expect_eq "$P.bin.corrupt.workdir" "başarısızlıktan sonra geçici dizin kalmadı" "" "$(ls -d /var/lib/myserver-install.* 2>/dev/null)"

# --- yarıda kalan kurulum: programlar kurulduktan SONRA hata ---
fresh_release v1.0.0
printf 'myserver ALL=(root NOPASSWD /usr/local/libexec/myserver-helper ((\n' >"$INSTALLER_DIR/packaging/myserver.sudoers"
run_installer "$I" --yes --no-docker
refused "$P.mid.fail" "geçersiz sudoers dosyası kurulmadan reddediliyor" 'sudoers dosyası doğrulanamadı'
expect_eq "$P.mid.sudoersd" "sudoers.d içinde bozuk/geçici dosya kalmadı" "" "$(ls -A /etc/sudoers.d | grep -i myserver || true)"
expect_ok "$P.mid.visudo" "visudo -c hâlâ geçiyor (sistemin sudo ayarı bozulmadı)" visudo -c
expect_ok "$P.mid.partial" "yarım kurulum: program dosyası yerinde (yeniden çalıştırma bunu tamamlamalı)" test -x /usr/local/bin/myserver
expect_eq "$P.mid.noservice" "yarım kurulumda servis başlatılmadı" "inactive" "$(systemctl is-active myserver 2>&1)"

fresh_release v1.0.0
run_installer "$I" --yes --no-docker
expect_eq "$P.mid.rerun" "yarım kalan kurulumdan sonra yeniden çalıştırma başarılı" "0" "$RC"
bash /opt/tests/verify_install.sh "$P.mid.v" 8080

# İndirme kipi sınamaları temiz durumdan başlasın.
bash /usr/local/share/myserver/scripts/uninstall.sh --level 2 --yes --confirm-data-loss >/tmp/uninst.log 2>&1
expect_nothing_installed "$P.reset"

# --- indirme kipi ---
start_https || fail "$P.https" "yerel https sunucusu" "başlatılamadı"
publish good v1.0.0 "$REL_DIR/v1.0.0/myserver-linux-amd64.tar.gz"
publish good latest "$REL_DIR/v1.0.0/myserver-linux-amd64.tar.gz"
publish bad  v1.0.0 "$REL_DIR/v1.0.0/myserver-linux-amd64.tar.gz" bad
publish nosum v1.0.0 "$REL_DIR/v1.0.0/myserver-linux-amd64.tar.gz"; rm -f /srv/rel/nosum/v1.0.0/SHA256SUMS
fresh_release v1.0.0
cp "$I" /srv/rel/install.sh
mkdir -p /root/solo && cp "$I" /root/solo/install.sh   # yanında sürüm dosyası olmayan kopya

run_installer /root/solo/install.sh --yes --no-docker
refused "$P.dl.nourl" "ne yerel dosya ne yayın adresi varken açıklayıcı iletiyle duruyor" 'Kurulacak dosyalar bulunamadı'

run_installer /root/solo/install.sh --yes --no-docker --release-url "$HTTPS_BASE/bad" --version v1.0.0
refused "$P.dl.badsum" "yanlış SHA-256 özetiyle kurulum durduruluyor" 'SHA-256 doğrulaması BAŞARISIZ'
expect_nothing_installed "$P.dl.badsum.clean"
expect_fail "$P.dl.badsum.nouser" "yanlış özet: kullanıcı oluşturulmadı" getent passwd myserver
expect_fail "$P.dl.badsum.noconf" "yanlış özet: /etc/myserver oluşturulmadı" test -e /etc/myserver
expect_eq "$P.dl.badsum.workdir" "yanlış özet: indirilen dosyalar temizlendi" "" "$(ls -d /var/lib/myserver-install.* 2>/dev/null)"

run_installer /root/solo/install.sh --yes --no-docker --release-url "$HTTPS_BASE/nosum" --version v1.0.0
refused "$P.dl.nosum" "SHA256SUMS yayımlanmamışsa kurulum durduruluyor" 'Özet dosyası indirilemedi'
expect_nothing_installed "$P.dl.nosum.clean"

run_installer /root/solo/install.sh --yes --no-docker --release-url "$HTTPS_BASE/redir-http/good" --version v1.0.0
refused "$P.dl.redir.http" "düz http'ye yönlendirme reddediliyor" 'indirilemedi'
expect_nothing_installed "$P.dl.redir.http.clean"

# Gerçek kullanım biçimi: curl ... | bash -s -- seçenekler
OUT=$(curl -fsSL "$HTTPS_BASE/install.sh" | bash -s -- --yes --no-docker --release-url "$HTTPS_BASE/redir/good" --version v1.0.0 2>&1); RC=$?
if (( RC == 0 )); then pass "$P.dl.good" "curl | bash ile indirme kipi (https yönlendirmesi izlenerek) başarılı"
else fail "$P.dl.good" "curl | bash ile indirme kipi başarılı" "rc=$RC $(tail -n 10 <<<"$OUT")"; fi
expect_match "$P.dl.good.sum" "indirme kipinde özet doğrulandı" 'SHA-256 özeti doğrulandı' "$OUT"
expect_eq "$P.dl.good.envurl" "yayın adresi ortam dosyasına yazıldı" "$HTTPS_BASE/redir/good" \
  "$(sed -n 's/^MYSERVER_RELEASE_URL=//p' /etc/myserver/myserver.env)"
bash /opt/tests/verify_install.sh "$P.dl.v" 8080

# MYSERVER_VERSION / latest
bash /usr/local/share/myserver/scripts/uninstall.sh --level 2 --yes --confirm-data-loss >/tmp/uninst.log 2>&1
OUT=$(curl -fsSL "$HTTPS_BASE/install.sh" | MYSERVER_RELEASE_URL="$HTTPS_BASE/good" bash -s -- --yes --no-docker 2>&1); RC=$?
expect_eq "$P.dl.latest" "ortam değişkeniyle adres + varsayılan 'latest' sürümü kuruluyor" "0" "$RC"
expect_eq "$P.dl.latest.ver" "kurulan sürüm 1.0.0" "1.0.0" "$(/usr/local/bin/myserver --version 2>&1)"
