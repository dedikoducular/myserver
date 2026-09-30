#!/usr/bin/env bash
# MyServer panel yedeği (felaket kurtarma kopyası).
#
# Panelin KENDİ durumunu yedekler: SQLite veritabanı (tutarlı kopya) ve
# /etc/myserver. Uygulama verilerinin yedeği panelin içindeki yedekleme
# bölümünden alınır; bu betik onları kapsamaz.
#
# Kullanım:
#   sudo /usr/local/share/myserver/scripts/backup.sh [--keep 10] [--output-dir <dizin>]

set -Eeuo pipefail

readonly BIN_PATH="/usr/local/bin/myserver"
readonly DATA_DIR_DEFAULT="/var/lib/myserver"
readonly CONF_DIR="/etc/myserver"
readonly ENV_FILE="/etc/myserver/myserver.env"
readonly SVC_USER="myserver"
readonly SVC_GROUP="myserver"
readonly SERVICE="myserver"
readonly PREFIX="myserver-panel-"

KEEP=10
OUTPUT_DIR=""
ALLOW_STOP=1
DATA_DIR="$DATA_DIR_DEFAULT"
CURRENT_STEP="başlangıç"
WORK_DIR=""
RESTART_NEEDED=0

info() { printf '%s\n' "$*"; }
warn() { printf 'UYARI: %s\n' "$*" >&2; }
die()  { trap - ERR; printf 'HATA: %s\n' "$*" >&2; exit 1; }

cleanup() {
  # Servis bu betik tarafından durdurulduysa her koşulda yeniden başlatılır.
  if (( RESTART_NEEDED )); then
    RESTART_NEEDED=0
    systemctl start "$SERVICE" || warn "Servis yeniden başlatılamadı: sudo systemctl start $SERVICE"
  fi
  if [[ -n "$WORK_DIR" && -d "$WORK_DIR" ]]; then rm -rf -- "$WORK_DIR"; fi
}
on_error() {
  local code=$1 line=$2
  trap - ERR
  printf 'HATA: Yedekleme "%s" adımında, %s. satırda durdu (çıkış kodu %s).\n' \
    "$CURRENT_STEP" "$line" "$code" >&2
  exit "$code"
}
trap 'on_error $? $LINENO' ERR
trap cleanup EXIT

usage() {
  cat <<'EOF'
MyServer panel yedeği

Kullanım:
  backup.sh [seçenekler]

Seçenekler:
  --keep <sayı>         Saklanacak en yeni yedek sayısı (varsayılan 10).
                        0 verilirse eski yedekler silinmez.
  --output-dir <dizin>  Yedeğin yazılacağı dizin
                        (varsayılan /var/lib/myserver/backups/panel).
  --no-stop             sqlite3 komutu yoksa servisi durdurmak yerine hata ver.
  --help                Bu yardımı göster.

Veritabanı tutarlılığı:
  Veritabanı WAL kipinde çalışır; servis çalışırken dosyayı düz kopyalamak
  bozuk bir kopya üretebilir. sqlite3 komutu kuruluysa SQLite'ın kendi yedekleme
  düzeneği kullanılır (servis durmaz). Kurulu değilse servis kısa süreliğine
  durdurulur, dosyalar kopyalanır ve servis yeniden başlatılır.

Geri yükleme (elle):
  sudo systemctl stop myserver
  sudo tar -xzf <yedek>.tar.gz -C /tmp/geri
  sudo rm -f /var/lib/myserver/myserver.db-wal /var/lib/myserver/myserver.db-shm
  sudo install -o myserver -g myserver -m 0600 /tmp/geri/db/myserver.db /var/lib/myserver/myserver.db
  sudo install -o root -g myserver -m 0640 /tmp/geri/etc/myserver/myserver.env /etc/myserver/myserver.env
  sudo systemctl start myserver
EOF
}

have() { command -v "$1" >/dev/null 2>&1; }

env_get() {
  local value=""
  [[ -f "$ENV_FILE" ]] || return 0
  value=$(sed -n "s/^${1}=//p" "$ENV_FILE" | tail -n 1)
  value=${value%$'\r'}
  value=${value#\"}; value=${value%\"}
  printf '%s' "$value"
}

parse_args() {
  while (( $# )); do
    case "$1" in
      --keep)         [[ $# -ge 2 ]] || die "--keep bir değer gerektirir."; KEEP=$2; shift 2 ;;
      --keep=*)       KEEP=${1#*=}; shift ;;
      --output-dir)   [[ $# -ge 2 ]] || die "--output-dir bir değer gerektirir."; OUTPUT_DIR=$2; shift 2 ;;
      --output-dir=*) OUTPUT_DIR=${1#*=}; shift ;;
      --no-stop)      ALLOW_STOP=0; shift ;;
      --help|-h)      usage; exit 0 ;;
      *)              usage >&2; die "Bilinmeyen seçenek: $1" ;;
    esac
  done
  [[ "$KEEP" =~ ^[0-9]{1,4}$ ]] || die "--keep değeri geçersiz: \"$KEEP\"."
  KEEP=$((10#$KEEP))
  if [[ -n "$OUTPUT_DIR" && "$OUTPUT_DIR" != /* ]]; then
    die "--output-dir mutlak bir yol olmalıdır."
  fi
}

in_service_cgroup() {
  [[ -r /proc/self/cgroup ]] && grep -q "/${SERVICE}\.service" /proc/self/cgroup
}

copy_database() {
  CURRENT_STEP="veritabanı kopyası"
  local db="$DATA_DIR/myserver.db" f
  mkdir -p "$WORK_DIR/stage/db"
  if [[ ! -f "$db" ]]; then
    warn "Veritabanı bulunamadı ($db); yalnızca yapılandırma yedeklenecek."
    return 0
  fi

  if have sqlite3; then
    # Servis çalışırken de tutarlı: SQLite'ın çevrimiçi yedekleme düzeneği.
    # Servis kullanıcısıyla çalıştırılır ki -wal/-shm dosyaları root'a geçmesin.
    local tmpdb="$DATA_DIR/tmp/panel-backup.$$.db"
    install -d -m 0750 -o "$SVC_USER" -g "$SVC_GROUP" "$DATA_DIR/tmp"
    rm -f -- "$tmpdb"
    if runuser -u "$SVC_USER" -- sqlite3 -cmd ".timeout 10000" "$db" ".backup '$tmpdb'"; then
      if [[ "$(runuser -u "$SVC_USER" -- sqlite3 "$tmpdb" 'PRAGMA integrity_check;' 2>/dev/null | head -n 1)" == "ok" ]]; then
        mv -f -- "$tmpdb" "$WORK_DIR/stage/db/myserver.db"
        info "Veritabanı SQLite yedekleme düzeneğiyle kopyalandı (servis durdurulmadı)."
        return 0
      fi
      warn "SQLite kopyası bütünlük denetiminden geçmedi; diğer yöntem denenecek."
    else
      warn "sqlite3 ile yedekleme başarısız oldu; diğer yöntem denenecek."
    fi
    rm -f -- "$tmpdb" "${tmpdb}-wal" "${tmpdb}-shm" "${tmpdb}-journal"
  fi

  if systemctl is-active --quiet "$SERVICE"; then
    (( ALLOW_STOP )) || die "sqlite3 kurulu değil ve --no-stop verildi; tutarlı kopya alınamıyor. \"sudo apt-get install sqlite3\" ile kurabilirsiniz."
    if in_service_cgroup; then
      die "Bu betik panelin içinden çalıştırıldı ve sqlite3 kurulu değil. Servisi durdurmak betiği de sonlandırırdı. sqlite3 kurun veya betiği SSH oturumundan çalıştırın."
    fi
    info "sqlite3 bulunamadı; servis kısa süreliğine durduruluyor..."
    RESTART_NEEDED=1
    systemctl stop "$SERVICE"
  fi
  # Servis durmuşken düz kopya tutarlıdır; WAL dosyaları da birlikte alınır.
  for f in myserver.db myserver.db-wal myserver.db-shm; do
    if [[ -f "$DATA_DIR/$f" ]]; then cp -p -- "$DATA_DIR/$f" "$WORK_DIR/stage/db/$f"; fi
  done
  if (( RESTART_NEEDED )); then
    RESTART_NEEDED=0
    systemctl start "$SERVICE"
    info "Servis yeniden başlatıldı."
  fi
}

copy_config() {
  CURRENT_STEP="yapılandırma kopyası"
  mkdir -p "$WORK_DIR/stage/etc"
  if [[ -d "$CONF_DIR" ]]; then
    cp -a -- "$CONF_DIR" "$WORK_DIR/stage/etc/myserver"
  else
    warn "$CONF_DIR bulunamadı."
  fi
  {
    printf 'created=%s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
    printf 'hostname=%s\n' "$(hostname 2>/dev/null || true)"
    printf 'version=%s\n' "$("$BIN_PATH" --version 2>/dev/null || true)"
    printf 'data_dir=%s\n' "$DATA_DIR"
  } >"$WORK_DIR/stage/MANIFEST"
}

write_archive() {
  CURRENT_STEP="arşivin yazılması"
  local name target tmp
  name="${PREFIX}$(date '+%Y%m%d-%H%M%S').tar.gz"
  target="$OUTPUT_DIR/$name"
  # Aynı saniye içinde ikinci bir yedek öncekinin üzerine yazmasın.
  while [[ -e "$target" ]]; do
    sleep 1
    name="${PREFIX}$(date '+%Y%m%d-%H%M%S').tar.gz"
    target="$OUTPUT_DIR/$name"
  done
  tmp="$OUTPUT_DIR/.${name}.tmp.$$"
  tar -czf "$tmp" -C "$WORK_DIR/stage" .
  tar -tzf "$tmp" >/dev/null
  if getent passwd "$SVC_USER" >/dev/null; then chown root:"$SVC_GROUP" "$tmp"; fi
  chmod 0640 "$tmp"
  mv -f -- "$tmp" "$target"
  info "Yedek oluşturuldu: $target ($(du -h -- "$target" | awk '{print $1}'))"
}

apply_retention() {
  CURRENT_STEP="eski yedeklerin silinmesi"
  (( KEEP > 0 )) || return 0
  local files=() f i
  while IFS= read -r f; do
    files+=("$f")
  done < <(find "$OUTPUT_DIR" -maxdepth 1 -type f -name "${PREFIX}*.tar.gz" -printf '%f\n' | sort -r)
  for (( i = KEEP; i < ${#files[@]}; i++ )); do
    rm -f -- "$OUTPUT_DIR/${files[$i]}"
    info "Eski yedek silindi: ${files[$i]}"
  done
}

main() {
  parse_args "$@"
  [[ ${EUID:-$(id -u)} -eq 0 ]] || die "Bu betik root yetkisiyle çalıştırılmalıdır."
  umask 077

  local custom
  custom=$(env_get MYSERVER_DATA_DIR)
  if [[ -n "$custom" && "$custom" == /* ]]; then DATA_DIR=$custom; fi
  [[ -d "$DATA_DIR" ]] || die "Veri dizini bulunamadı: $DATA_DIR"
  if [[ -z "$OUTPUT_DIR" ]]; then OUTPUT_DIR="$DATA_DIR/backups/panel"; fi

  if [[ ! -d "$OUTPUT_DIR" ]]; then
    if getent passwd "$SVC_USER" >/dev/null; then
      install -d -m 0750 -o "$SVC_USER" -g "$SVC_GROUP" "$OUTPUT_DIR"
    else
      install -d -m 0700 "$OUTPUT_DIR"
    fi
  fi

  WORK_DIR=$(mktemp -d "$OUTPUT_DIR/.work.XXXXXX")
  chmod 0700 "$WORK_DIR"

  copy_database
  copy_config
  write_archive
  apply_retention
}

main "$@"
