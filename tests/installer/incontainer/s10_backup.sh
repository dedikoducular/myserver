#!/usr/bin/env bash
# Senaryo 10: backup.sh. Kurulu panel ve tamamlanmış sihirbaz gerektirir.
set -uo pipefail
. /opt/tests/lib.sh
P=s10
B=/usr/local/share/myserver/scripts/backup.sh
OUTD=/var/lib/myserver/backups/panel

latest() { ls -1 "$OUTD"/myserver-panel-*.tar.gz 2>/dev/null | sort | tail -n 1; }
check_archive() { # <kimlik> <arşiv>
  local id=$1 a=$2 x
  expect_stat "$id.stat" "$a" root:myserver 640
  x=$(mktemp -d /root/restore.XXXXXX)
  if tar -xzf "$a" -C "$x"; then pass "$id.extract" "arşiv açılabiliyor"; else fail "$id.extract" "arşiv açılabiliyor" ""; fi
  expect_ok "$id.has.db" "arşivde db/myserver.db var" test -s "$x/db/myserver.db"
  expect_ok "$id.has.env" "arşivde etc/myserver/myserver.env var" test -s "$x/etc/myserver/myserver.env"
  expect_ok "$id.has.manifest" "arşivde MANIFEST var" grep -q '^version=' "$x/MANIFEST"
  expect_eq "$id.integrity" "veritabanı kopyası tutarlı (PRAGMA integrity_check)" "ok" \
    "$(sqlite3 "$x/db/myserver.db" 'PRAGMA integrity_check;' 2>&1)"
  expect_eq "$id.users" "kopyada yönetici hesabı var" "$ADMIN_USER" \
    "$(sqlite3 "$x/db/myserver.db" 'SELECT username FROM users ORDER BY id LIMIT 1;' 2>&1)"
  RESTORE_DIR=$x
}

# --- servis çalışırken ve yazarken (sqlite3 ile çevrimiçi yedek) ---
( for _ in $(seq 1 400); do api_login 8080 >/dev/null 2>&1; done ) &
writer=$!
sleep 1
pid_before=$(systemctl show -p MainPID --value myserver)
for n in 1 2 3; do
  out=$(bash "$B" 2>&1); rc=$?
  expect_eq "$P.live$n.rc" "yazma sürerken yedek #$n başarılı" "0" "$rc"
  expect_match "$P.live$n.method" "SQLite yedekleme düzeneği kullanıldı (servis durdurulmadı)" 'servis durdurulmadı' "$out"
  check_archive "$P.live$n" "$(latest)"
  rm -rf "$RESTORE_DIR"
done
kill "$writer" 2>/dev/null; wait "$writer" 2>/dev/null
expect_eq "$P.live.pid" "yedekleme sırasında servis yeniden başlatılmadı" "$pid_before" "$(systemctl show -p MainPID --value myserver)"
expect_eq "$P.live.count" "üç ayrı yedek dosyası oluştu (aynı saniyede üzerine yazılmadı)" "3" "$(ls -1 "$OUTD"/myserver-panel-*.tar.gz | wc -l | tr -d ' ')"
expect_eq "$P.live.tmp" "tmp/ içinde geçici veritabanı kalmadı" "" "$(ls /var/lib/myserver/tmp | grep panel-backup || true)"
expect_eq "$P.live.work" "çıktı dizininde geçici dosya/dizin kalmadı" "" "$(ls -A "$OUTD" | grep -v '^myserver-panel-.*\.tar\.gz$' || true)"
expect_eq "$P.live.walowner" "WAL dosyaları root'a geçmedi" "" \
  "$(find /var/lib/myserver -maxdepth 1 -name 'myserver.db*' ! -user myserver)"
expect_fail "$P.perm.nobody" "yetkisiz kullanıcı yedeği okuyamıyor" runuser -u nobody -- cat "$(latest)"
expect_stat "$P.perm.dir" "$OUTD" myserver:myserver 750

# --- geri yükleme (yardım metnindeki adımlar) ---
check_archive "$P.restore.src" "$(latest)"
systemctl stop myserver
rm -f /var/lib/myserver/myserver.db-wal /var/lib/myserver/myserver.db-shm
install -o myserver -g myserver -m 0600 "$RESTORE_DIR/db/myserver.db" /var/lib/myserver/myserver.db
install -o root -g myserver -m 0640 "$RESTORE_DIR/etc/myserver/myserver.env" /etc/myserver/myserver.env
systemctl start myserver
expect_ok "$P.restore.http" "geri yüklemeden sonra panel yanıt veriyor" wait_http 8080 30
expect_match "$P.restore.login" "geri yüklenen veritabanıyla yönetici oturum açabiliyor" '"authenticated":true' "$(api_login 8080)"
rm -rf "$RESTORE_DIR"

# --- sqlite3 yokken: servis kısa süreliğine durdurulur ---
mv /usr/bin/sqlite3 /usr/bin/sqlite3.hidden
out=$(bash "$B" --no-stop 2>&1); rc=$?
n_before=$(ls -1 "$OUTD"/myserver-panel-*.tar.gz | wc -l | tr -d ' ')
if (( rc != 0 )) && grep -q 'tutarlı kopya alınamıyor' <<<"$out"; then pass "$P.nostop" "sqlite3 yok + --no-stop: hata veriyor"
else fail "$P.nostop" "sqlite3 yok + --no-stop: hata veriyor" "rc=$rc $out"; fi
expect_eq "$P.nostop.svc" "--no-stop: servis durdurulmadı" "active" "$(systemctl is-active myserver)"
expect_eq "$P.nostop.work" "--no-stop: geçici dizin kalmadı" "" "$(ls -A "$OUTD" | grep -v '^myserver-panel-.*\.tar\.gz$' || true)"
out=$(bash "$B" 2>&1); rc=$?
mv /usr/bin/sqlite3.hidden /usr/bin/sqlite3
expect_eq "$P.stop.rc" "sqlite3 yokken yedek başarılı" "0" "$rc"
expect_match "$P.stop.msg" "servisin durdurulup başlatıldığı bildirildi" 'Servis yeniden başlatıldı' "$out"
expect_ok "$P.stop.http" "servis yeniden çalışıyor" wait_http 8080 30
expect_eq "$P.stop.new" "yeni yedek dosyası oluştu" "$((n_before + 1))" "$(ls -1 "$OUTD"/myserver-panel-*.tar.gz | wc -l | tr -d ' ')"
a=$(latest); x=$(mktemp -d /root/restore.XXXXXX); tar -xzf "$a" -C "$x"
expect_stat "$P.stop.stat" "$a" root:myserver 640
expect_eq "$P.stop.integrity" "düz kopya (WAL ile birlikte) tutarlı" "ok" "$(sqlite3 "$x/db/myserver.db" 'PRAGMA integrity_check;' 2>&1)"
expect_eq "$P.stop.users" "düz kopyada yönetici hesabı var" "$ADMIN_USER" "$(sqlite3 "$x/db/myserver.db" 'SELECT username FROM users ORDER BY id LIMIT 1;' 2>&1)"
rm -rf "$x"

# --- saklama (retention): yalnızca eski panel yedekleri silinir ---
R=/root/retention; rm -rf "$R"; mkdir -p "$R/altdizin"
for d in 01 02 03 04 05 06 07 08; do echo x >"$R/myserver-panel-202001${d}-000000.tar.gz"; done
echo keep >"$R/baska-yedek.tar.gz"
echo keep >"$R/myserver-panel-notlar.txt"
echo keep >"$R/myserver-app-20200101-000000.tar.gz"
echo keep >"$R/altdizin/myserver-panel-20190101-000000.tar.gz"
mkdir "$R/myserver-panel-20180101-000000.tar.gz.d"
out=$(bash "$B" --keep 3 --output-dir "$R" 2>&1); rc=$?
expect_eq "$P.ret.rc" "--keep 3 --output-dir başarılı" "0" "$rc"
expect_eq "$P.ret.kept" "en yeni 3 panel yedeği kaldı (yeni + 08 + 07)" \
  "NEW myserver-panel-20200107-000000.tar.gz myserver-panel-20200108-000000.tar.gz" \
  "$(ls -1 "$R" | grep -E '^myserver-panel-[0-9]{8}-[0-9]{6}\.tar\.gz$' | sed -E "s/^myserver-panel-$(date +%Y)[0-9]{4}-[0-9]{6}\.tar\.gz$/NEW/" | LC_ALL=C sort | tr '\n' ' ' | sed 's/ $//')"
expect_eq "$P.ret.others" "panel yedeği olmayan dosya ve dizinlere dokunulmadı" \
  "altdizin baska-yedek.tar.gz myserver-app-20200101-000000.tar.gz myserver-panel-20180101-000000.tar.gz.d myserver-panel-notlar.txt" \
  "$(ls -1 "$R" | grep -vE '^myserver-panel-[0-9]{8}-[0-9]{6}\.tar\.gz$' | sort | tr '\n' ' ' | sed 's/ $//')"
expect_ok "$P.ret.subdir" "alt dizindeki dosya silinmedi" test -f "$R/altdizin/myserver-panel-20190101-000000.tar.gz"
n=$(ls -1 "$R" | wc -l)
bash "$B" --keep 0 --output-dir "$R" >/dev/null 2>&1
expect_eq "$P.ret.keep0" "--keep 0 hiçbir şeyi silmedi (yalnızca yeni yedek eklendi)" "$((n + 1))" "$(ls -1 "$R" | wc -l | tr -d ' ')"

# --- bağımsız değişkenler ---
for bad in -1 abc 1.5 99999 '3;id' ''; do
  out=$(bash "$B" --keep "$bad" 2>&1); rc=$?
  if (( rc != 0 )) && grep -q 'geçersiz' <<<"$out"; then pass "$P.arg.keep[$bad]" "geçersiz --keep reddediliyor: '$bad'"
  else fail "$P.arg.keep[$bad]" "geçersiz --keep reddediliyor: '$bad'" "rc=$rc $out"; fi
done
out=$(bash "$B" --output-dir goreli/yol 2>&1); rc=$?
if (( rc != 0 )) && grep -q 'mutlak bir yol' <<<"$out"; then pass "$P.arg.rel" "göreli --output-dir reddediliyor"
else fail "$P.arg.rel" "göreli --output-dir reddediliyor" "rc=$rc $out"; fi
OUT=$(runuser -u nobody -- bash "$B" 2>&1); rc=$?
if (( rc != 0 )) && grep -q 'root yetkisiyle' <<<"$OUT"; then pass "$P.nonroot" "root olmayan kullanıcıyla reddediliyor"
else fail "$P.nonroot" "root olmayan kullanıcıyla reddediliyor" "rc=$rc $OUT"; fi
expect_eq "$P.end.svc" "senaryo sonunda servis çalışıyor" "active" "$(systemctl is-active myserver)"
