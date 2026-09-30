#!/usr/bin/env bash
# MyServer güncelleme betiği.
#
# Kullanım:
#   sudo /usr/local/share/myserver/scripts/update.sh <sürüm>
#   sudo /usr/local/share/myserver/scripts/update.sh --from <dizin | arşiv.tar.gz>
#
# Panelin root yardımcısı bu betiği "update.sh <sürüm>" biçiminde, ayrı bir
# systemd birimi (myserver-update) içinde çalıştırır.
#
# Yapılanlar: özet doğrulama -> önceki sürümün saklanması -> servisin
# durdurulması -> atomik kurulum -> veritabanı güncellemesi -> servisin
# başlatılması -> yanıt denetimi. Panel ayağa kalkmazsa önceki sürüme ve
# güncelleme öncesi veritabanına otomatik olarak geri dönülür.

set -Eeuo pipefail

readonly BIN_PATH="/usr/local/bin/myserver"
readonly HELPER_PATH="/usr/local/libexec/myserver-helper"
readonly SHARE_DIR="/usr/local/share/myserver"
readonly ROLLBACK_DIR="/usr/local/share/myserver/rollback"
readonly DATA_DIR_DEFAULT="/var/lib/myserver"
readonly ENV_FILE="/etc/myserver/myserver.env"
readonly UNIT_PATH="/etc/systemd/system/myserver.service"
readonly SVC_USER="myserver"
readonly SVC_GROUP="myserver"
readonly SERVICE="myserver"
readonly UPDATE_UNIT="myserver-update"
readonly LOCK_FILE="/run/myserver-update.lock"
readonly DEFAULT_PORT="8080"
readonly START_TIMEOUT=60

VERSION_ARG=""
ENV_VERSION="${MYSERVER_UPDATE_VERSION:-}"
FROM_PATH=""
EXPECT_SHA="${MYSERVER_UPDATE_SHA256:-}"
DIRECT_URL="${MYSERVER_UPDATE_URL:-}"
RELEASE_URL="${MYSERVER_RELEASE_URL:-}"
SKIP_VERIFY=0
DETACHED=0

CURRENT_STEP="başlangıç"
WORK_DIR=""
SRC_DIR=""
ARCH=""
DATA_DIR="$DATA_DIR_DEFAULT"
STATUS_FILE=""
PORT=""
LISTEN_HOST=""
OLD_VERSION=""
NEW_VERSION=""
CHANGED=0
ROLLING_BACK=0
WAS_ACTIVE=0
HAD_DB=0

log()  { printf '%s %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*"; }
warn() { printf '%s UYARI: %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*" >&2; }

write_status() {
  # state: running | success | failed | rolled_back
  local state=$1 message=$2 tmp
  [[ -n "$STATUS_FILE" && -d "$(dirname -- "$STATUS_FILE")" ]] || return 0
  tmp="${STATUS_FILE}.tmp.$$"
  {
    printf 'state=%s\n' "$state"
    printf 'target=%s\n' "${VERSION_ARG:-${FROM_PATH:-}}"
    printf 'from_version=%s\n' "$OLD_VERSION"
    printf 'to_version=%s\n' "$NEW_VERSION"
    printf 'time=%s\n' "$(date +%s)"
    printf 'message=%s\n' "$message"
  } >"$tmp" 2>/dev/null || return 0
  chown "$SVC_USER":"$SVC_GROUP" "$tmp" 2>/dev/null || true
  chmod 0640 "$tmp" 2>/dev/null || true
  mv -f -- "$tmp" "$STATUS_FILE" 2>/dev/null || true
}

cleanup() {
  if [[ -n "$WORK_DIR" && -d "$WORK_DIR" ]]; then rm -rf -- "$WORK_DIR"; fi
}

die() {
  trap - ERR
  printf '%s HATA: %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*" >&2
  if (( CHANGED && ! ROLLING_BACK )); then
    rollback "$*"
  fi
  write_status failed "$*"
  exit 1
}

on_error() {
  local code=$1 line=$2
  trap - ERR
  printf '%s HATA: Güncelleme "%s" adımında, %s. satırda durdu (çıkış kodu %s).\n' \
    "$(date '+%Y-%m-%d %H:%M:%S')" "$CURRENT_STEP" "$line" "$code" >&2
  if (( CHANGED && ! ROLLING_BACK )); then
    rollback "\"$CURRENT_STEP\" adımı başarısız oldu"
  fi
  write_status failed "Güncelleme \"$CURRENT_STEP\" adımında başarısız oldu."
  exit "$code"
}
trap 'on_error $? $LINENO' ERR
trap cleanup EXIT

usage() {
  cat <<'EOF'
MyServer güncelleme betiği

Kullanım:
  update.sh <sürüm>                 Örnek: update.sh v1.2.3  (veya 1.2.3)
  update.sh --from <dizin|arşiv>    Açılmış sürüm dizini ya da .tar.gz arşivi

Seçenekler:
  --release-url <url>   Sürümlerin indirileceği temel https adresi. Verilmezse
                        MYSERVER_RELEASE_URL ortam değişkeni, o da yoksa
                        /etc/myserver/myserver.env içindeki değer kullanılır.
  --sha256 <özet>       Arşivin beklenen SHA-256 özeti (64 onaltılık karakter).
                        İndirilen sürümlerde yayımlanan SHA256SUMS dosyası her
                        zaman denetlenir; özet de verildiyse ikisi de tutmalıdır.
  --skip-verify         YALNIZCA --from <dizin> için: dizinde SHA256SUMS yoksa
                        doğrulamasız devam et (kendi derlediğiniz dosyalar için).
  --help                Bu yardımı göster.

Ortam değişkenleri (panelin yardımcısı tarafından kullanılır):
  MYSERVER_UPDATE_VERSION, MYSERVER_UPDATE_SHA256, MYSERVER_UPDATE_URL
EOF
}

have() { command -v "$1" >/dev/null 2>&1; }

env_get() {
  local key=$1 value=""
  [[ -f "$ENV_FILE" ]] || return 0
  value=$(sed -n "s/^${key}=//p" "$ENV_FILE" | tail -n 1)
  value=${value%$'\r'}
  value=${value#\"}; value=${value%\"}
  value=${value#\'}; value=${value%\'}
  printf '%s' "$value"
}

install_file() {
  local src=$1 dst=$2 mode=$3 owner=$4 group=$5 tmp
  tmp="${dst}.new.$$"
  install -m "$mode" -o "$owner" -g "$group" -- "$src" "$tmp"
  mv -f -- "$tmp" "$dst"
}

sum_for() {
  # $1: SHA256SUMS dosyası, $2: dosya adı
  awk -v n="$2" '{ f = $2; sub(/^\*/, "", f); if (f == n) { print tolower($1); exit } }' "$1"
}

sha_of() { sha256sum -- "$1" | awk '{print tolower($1)}'; }

parse_args() {
  while (( $# )); do
    case "$1" in
      --from)          [[ $# -ge 2 ]] || die "--from bir değer gerektirir."; FROM_PATH=$2; shift 2 ;;
      --from=*)        FROM_PATH=${1#*=}; shift ;;
      --release-url)   [[ $# -ge 2 ]] || die "--release-url bir değer gerektirir."; RELEASE_URL=$2; shift 2 ;;
      --release-url=*) RELEASE_URL=${1#*=}; shift ;;
      --sha256)        [[ $# -ge 2 ]] || die "--sha256 bir değer gerektirir."; EXPECT_SHA=$2; shift 2 ;;
      --sha256=*)      EXPECT_SHA=${1#*=}; shift ;;
      --skip-verify)   SKIP_VERIFY=1; shift ;;
      --detached)      DETACHED=1; shift ;;
      --help|-h)       usage; exit 0 ;;
      -*)              usage >&2; die "Bilinmeyen seçenek: $1" ;;
      *)
        [[ -z "$VERSION_ARG" ]] || die "Birden fazla sürüm verildi."
        VERSION_ARG=$1; shift ;;
    esac
  done

  # Katı doğrulama: yalnızca anlamsal sürüm (isteğe bağlı "v" öneki). Hem
  # bağımsız değişken hem de MYSERVER_UPDATE_VERSION kullanılmadan önce doğrulanır.
  local semver='^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z][0-9A-Za-z.-]*)?$'
  local raw
  for raw in "$VERSION_ARG" "$ENV_VERSION"; do
    [[ -n "$raw" ]] || continue
    if (( ${#raw} > 64 )) || [[ ! "$raw" =~ $semver ]]; then
      VERSION_ARG=""
      die "Sürüm numarası geçersiz. Beklenen biçim: v1.2.3 veya 1.2.3"
    fi
  done
  if [[ -n "$VERSION_ARG" ]]; then VERSION_ARG="v${VERSION_ARG#v}"; fi
  if [[ -n "$ENV_VERSION" ]]; then ENV_VERSION="v${ENV_VERSION#v}"; fi

  if [[ -n "$FROM_PATH" ]]; then
    [[ -z "$VERSION_ARG" ]] || die "--from ile birlikte sürüm numarası verilemez."
    ENV_VERSION=""
  else
    if [[ -n "$VERSION_ARG" && -n "$ENV_VERSION" && "$VERSION_ARG" != "$ENV_VERSION" ]]; then
      VERSION_ARG=""
      die "Sürüm bağımsız değişkeni ile MYSERVER_UPDATE_VERSION birbirini tutmuyor; güncelleme yapılmadı."
    fi
    if [[ -z "$VERSION_ARG" ]]; then VERSION_ARG=$ENV_VERSION; fi
    if [[ -z "$VERSION_ARG" ]]; then
      usage >&2
      die "Bir sürüm numarası veya --from <yol> vermelisiniz."
    fi
  fi

  if [[ -n "$EXPECT_SHA" ]]; then
    # Yardımcı küçük harfli özet gönderir; elle kullanımda büyük harf de kabul edilir.
    [[ "$EXPECT_SHA" =~ ^[0-9a-fA-F]{64}$ ]] || die "SHA-256 özeti geçersiz (64 onaltılık karakter olmalı)."
    EXPECT_SHA=${EXPECT_SHA,,}
  fi
  if [[ -z "$RELEASE_URL" ]]; then RELEASE_URL=$(env_get MYSERVER_RELEASE_URL); fi
  RELEASE_URL=${RELEASE_URL%/}
  if [[ -n "$RELEASE_URL" && ! "$RELEASE_URL" =~ ^https://[A-Za-z0-9._:/%+~-]+$ ]]; then
    die "Yayın adresi geçersiz. Yalnızca https:// ile başlayan bir adres kabul edilir."
  fi
  if [[ -n "$DIRECT_URL" ]]; then
    [[ "$DIRECT_URL" =~ ^https://[A-Za-z0-9._:/%+~-]+$ ]] || die "MYSERVER_UPDATE_URL geçersiz."
    [[ -n "$EXPECT_SHA" ]] || die "MYSERVER_UPDATE_URL kullanılırken SHA-256 özeti de verilmelidir."
  fi
}

# "systemctl stop myserver" servis kontrol grubundaki TÜM süreçleri sonlandırır.
# Betik panelin alt süreci olarak (örneğin web terminalinden) başlatıldıysa
# kendisi de ölürdü; bu durumda ayrı bir systemd birimine taşınır.
detach_if_needed() {
  (( DETACHED )) && return 0
  [[ -r /proc/self/cgroup ]] || return 0
  grep -q "/${SERVICE}\.service" /proc/self/cgroup || return 0
  have systemd-run || die "Betik panel servisinin içinden çalıştırıldı ve systemd-run bulunamadı. Güncellemeyi SSH oturumundan başlatın."
  if systemctl is-active --quiet "${UPDATE_UNIT}.service"; then
    die "Bir MyServer güncellemesi zaten çalışıyor."
  fi
  local self args=()
  self=$(readlink -f -- "${BASH_SOURCE[0]}")
  if [[ -n "$FROM_PATH" ]]; then args+=(--from "$(readlink -f -- "$FROM_PATH")"); else args+=("$VERSION_ARG"); fi
  if [[ -n "$EXPECT_SHA" ]]; then args+=(--sha256 "$EXPECT_SHA"); fi
  if [[ -n "$RELEASE_URL" ]]; then args+=(--release-url "$RELEASE_URL"); fi
  if (( SKIP_VERIFY )); then args+=(--skip-verify); fi
  log "Betik panel servisinin içinden başlatıldı; güncelleme ayrı bir birimde sürdürülecek: ${UPDATE_UNIT}.service"
  log "İzlemek için: journalctl -u ${UPDATE_UNIT} -f"
  trap - ERR
  exec systemd-run --unit="$UPDATE_UNIT" --collect --quiet --no-block \
    --description="MyServer güncellemesi" \
    ${DIRECT_URL:+--setenv=MYSERVER_UPDATE_URL="$DIRECT_URL"} \
    -- /bin/bash "$self" --detached "${args[@]}"
}

preflight() {
  CURRENT_STEP="ön denetim"
  [[ ${EUID:-$(id -u)} -eq 0 ]] || die "Bu betik root yetkisiyle çalıştırılmalıdır."
  [[ -x "$BIN_PATH" && -x "$HELPER_PATH" && -f "$UNIT_PATH" ]] \
    || die "Mevcut bir MyServer kurulumu bulunamadı. Önce install.sh ile kurulum yapın."
  getent passwd "$SVC_USER" >/dev/null || die "\"$SVC_USER\" kullanıcısı bulunamadı; kurulum eksik."

  case "$(uname -m)" in
    x86_64|amd64)  ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) die "Desteklenmeyen işlemci mimarisi: $(uname -m)" ;;
  esac

  local custom listen
  custom=$(env_get MYSERVER_DATA_DIR)
  if [[ -n "$custom" && "$custom" == /* ]]; then DATA_DIR=$custom; fi
  STATUS_FILE="$DATA_DIR/update.status"
  listen=$(env_get MYSERVER_LISTEN)
  if [[ "$listen" =~ ^(.*):([0-9]{1,5})$ ]]; then
    LISTEN_HOST=${BASH_REMATCH[1]}
    PORT=${BASH_REMATCH[2]}
  fi
  if [[ -z "$PORT" ]]; then PORT=$DEFAULT_PORT; fi
  if [[ -z "$LISTEN_HOST" || "$LISTEN_HOST" == "0.0.0.0" || "$LISTEN_HOST" == "[::]" || "$LISTEN_HOST" == "*" ]]; then
    LISTEN_HOST="127.0.0.1"
  fi
  OLD_VERSION=$("$BIN_PATH" --version 2>/dev/null || true)
}

source_complete() {
  local dir=$1
  [[ -f "$dir/myserver-linux-$ARCH" && -f "$dir/myserver-helper-linux-$ARCH" \
     && -f "$dir/packaging/myserver.service" && -f "$dir/scripts/update.sh" ]]
}

extract_tarball() {
  local tarball=$1 entry candidate
  while IFS= read -r entry; do
    if [[ "$entry" == /* || "$entry" == *".."* ]]; then
      die "Arşiv güvenli olmayan bir yol içeriyor: $entry"
    fi
  done < <(tar -tzf "$tarball")
  mkdir -p "$WORK_DIR/release"
  tar -xzf "$tarball" -C "$WORK_DIR/release" --no-same-owner --no-same-permissions
  for candidate in "$WORK_DIR/release/myserver" "$WORK_DIR/release"; do
    if source_complete "$candidate"; then SRC_DIR=$candidate; return 0; fi
  done
  die "Arşivin içeriği beklenen düzende değil (bu mimari için dosyalar eksik: $ARCH)."
}

verify_tarball() {
  # $1: arşiv, $2: SHA256SUMS yolu, $3: 1 ise SHA256SUMS zorunlu
  local tarball=$1 sums=$2 require_sums=$3 name published="" actual
  name=$(basename -- "$tarball")
  if [[ -f "$sums" ]]; then
    published=$(sum_for "$sums" "$name")
    [[ "$published" =~ ^[0-9a-f]{64}$ ]] || die "SHA256SUMS içinde $name için geçerli bir özet yok."
  elif (( require_sums )); then
    die "Yayımlanmış SHA256SUMS dosyası bulunamadı; arşiv doğrulanamıyor."
  fi
  if [[ -z "$published" && -z "$EXPECT_SHA" ]]; then
    die "Arşivin yanında SHA256SUMS yok ve özet verilmedi; doğrulanamıyor. --sha256 ile özeti verin."
  fi
  if [[ -n "$published" && -n "$EXPECT_SHA" && "$published" != "$EXPECT_SHA" ]]; then
    die "Verilen SHA-256 özeti, yayımlanan SHA256SUMS dosyasındaki özetle çelişiyor; güncelleme yapılmadı."
  fi
  actual=$(sha_of "$tarball")
  if [[ -n "$EXPECT_SHA" && "$actual" != "$EXPECT_SHA" ]] || [[ -n "$published" && "$actual" != "$published" ]]; then
    die "SHA-256 doğrulaması BAŞARISIZ. Arşiv bozuk veya değiştirilmiş; güncelleme yapılmadı."
  fi
  log "SHA-256 özeti doğrulandı: $name"
}

verify_dir() {
  local dir=$1 name expected actual checked=0
  if [[ ! -f "$dir/SHA256SUMS" ]]; then
    (( SKIP_VERIFY )) || die "$dir içinde SHA256SUMS yok; dosyalar doğrulanamıyor. Kendi derlemeniz ise --skip-verify kullanın."
    warn "SHA256SUMS yok; --skip-verify verildiği için doğrulamasız devam ediliyor."
    return 0
  fi
  for name in "myserver-linux-$ARCH" "myserver-helper-linux-$ARCH"; do
    expected=$(sum_for "$dir/SHA256SUMS" "$name")
    [[ "$expected" =~ ^[0-9a-f]{64}$ ]] || die "SHA256SUMS içinde $name için geçerli bir özet yok."
    actual=$(sha_of "$dir/$name")
    [[ "$expected" == "$actual" ]] || die "SHA-256 doğrulaması BAŞARISIZ: $name"
    checked=$((checked + 1))
  done
  log "SHA-256 özetleri doğrulandı ($checked dosya)"
}

download() {
  # Yönlendirmeler izlenir (GitHub sürüm dosyaları başka bir sunucuya
  # yönlendirir) ancak yalnızca https adreslerine; düz http'ye yönlendirme reddedilir.
  curl -fsSL --proto '=https' --proto-redir '=https' --max-redirs 5 --tlsv1.2 \
    --retry 3 --connect-timeout 20 -o "$2" "$1"
}

obtain_source() {
  CURRENT_STEP="sürüm dosyalarının alınması"
  # /tmp "noexec" olabilir; ikili dosyalar sınanacağı için /var/lib kullanılır.
  WORK_DIR=$(mktemp -d /var/lib/myserver-update.XXXXXX)
  chmod 0700 "$WORK_DIR"
  local tarball="myserver-linux-$ARCH.tar.gz"

  if [[ -n "$FROM_PATH" ]]; then
    if [[ -d "$FROM_PATH" ]]; then
      local dir candidate
      dir=$(cd -- "$FROM_PATH" && pwd -P)
      for candidate in "$dir" "$dir/dist"; do
        if source_complete "$candidate"; then SRC_DIR=$candidate; break; fi
      done
      [[ -n "$SRC_DIR" ]] || die "$FROM_PATH dizininde bu mimari ($ARCH) için sürüm dosyaları bulunamadı."
      verify_dir "$SRC_DIR"
    elif [[ -f "$FROM_PATH" ]]; then
      cp -- "$FROM_PATH" "$WORK_DIR/$(basename -- "$FROM_PATH")"
      verify_tarball "$WORK_DIR/$(basename -- "$FROM_PATH")" "$(dirname -- "$FROM_PATH")/SHA256SUMS" 0
      extract_tarball "$WORK_DIR/$(basename -- "$FROM_PATH")"
    else
      die "Kaynak bulunamadı: $FROM_PATH"
    fi
    return 0
  fi

  have curl || die "curl bulunamadı."
  local base
  if [[ -n "$DIRECT_URL" ]]; then
    # Arşivin tam adresi verildi; SHA256SUMS aynı dizinden alınır.
    base=${DIRECT_URL%/*}
    log "İndiriliyor: $DIRECT_URL"
    download "$DIRECT_URL" "$WORK_DIR/$tarball" || die "Sürüm arşivi indirilemedi."
  else
    [[ -n "$RELEASE_URL" ]] || die "Yayın adresi ayarlanmamış. $ENV_FILE dosyasına MYSERVER_RELEASE_URL ekleyin veya --release-url kullanın; ya da --from ile yerel bir sürüm verin."
    base=$(release_base "$RELEASE_URL" "$VERSION_ARG")
    log "İndiriliyor: $base/$tarball"
    download "$base/$tarball" "$WORK_DIR/$tarball" || die "Sürüm arşivi indirilemedi: $base/$tarball"
  fi
  # İndirilen sürümlerde yayımlanan SHA256SUMS her zaman zorunludur;
  # MYSERVER_UPDATE_SHA256 verildiyse ikisi de tutmalıdır.
  download "$base/SHA256SUMS" "$WORK_DIR/SHA256SUMS" || die "Özet dosyası indirilemedi: $base/SHA256SUMS"
  verify_tarball "$WORK_DIR/$tarball" "$WORK_DIR/SHA256SUMS" 1
  extract_tarball "$WORK_DIR/$tarball"
}

# release_base <yayın adresi> <sürüm>: sürüm dosyalarının bulunduğu dizinin
# adresi. GitHub Releases adresi (https://github.com/<kişi>/<depo>/releases)
# verildiyse GitHub'ın kendi düzeni kullanılır:
#   latest -> <adres>/latest/download     v1.2.3 -> <adres>/download/v1.2.3
# Diğer sunucularda düzen <adres>/<sürüm> şeklindedir.
release_base() {
  if [[ "$1" =~ ^https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/releases$ ]]; then
    if [[ "$2" == latest ]]; then printf '%s/latest/download' "$1"; else printf '%s/download/%s' "$1" "$2"; fi
  else
    printf '%s/%s' "$1" "$2"
  fi
}

probe_new() {
  CURRENT_STEP="yeni sürümün sınanması"
  install -m 0755 -- "$SRC_DIR/myserver-linux-$ARCH" "$WORK_DIR/probe"
  install -m 0755 -- "$SRC_DIR/myserver-helper-linux-$ARCH" "$WORK_DIR/probe-helper"
  NEW_VERSION=$("$WORK_DIR/probe" --version 2>/dev/null) \
    || die "Yeni panel dosyası bu sistemde çalıştırılamadı."
  [[ -n "$NEW_VERSION" ]] || die "Yeni panel dosyası sürüm bilgisi vermedi."
  "$WORK_DIR/probe-helper" list-actions >/dev/null 2>&1 \
    || die "Yeni yardımcı program bu sistemde çalıştırılamadı."
  if [[ -n "$VERSION_ARG" && "${NEW_VERSION#v}" != "${VERSION_ARG#v}" ]]; then
    die "İndirilen dosyanın sürümü ($NEW_VERSION) istenen sürümle ($VERSION_ARG) eşleşmiyor."
  fi
  log "Geçerli sürüm: ${OLD_VERSION:-bilinmiyor}, yeni sürüm: $NEW_VERSION"
}

save_previous() {
  CURRENT_STEP="önceki sürümün saklanması"
  local stage="${ROLLBACK_DIR}.new.$$"
  rm -rf -- "$stage"
  install -d -m 0700 -o root -g root "$stage"
  install -m 0755 -o root -g root -- "$BIN_PATH" "$stage/myserver"
  install -m 0755 -o root -g root -- "$HELPER_PATH" "$stage/myserver-helper"
  install -m 0644 -o root -g root -- "$UNIT_PATH" "$stage/myserver.service"
  printf '%s\n' "$OLD_VERSION" >"$stage/VERSION"
  rm -rf -- "${ROLLBACK_DIR}.old"
  if [[ -d "$ROLLBACK_DIR" ]]; then mv -- "$ROLLBACK_DIR" "${ROLLBACK_DIR}.old"; fi
  mv -- "$stage" "$ROLLBACK_DIR"
  rm -rf -- "${ROLLBACK_DIR}.old"
  log "Önceki sürüm saklandı: $ROLLBACK_DIR"
}

snapshot_db() {
  # Servis durdurulmuşken çağrılır; bu nedenle düz dosya kopyası tutarlıdır.
  local f
  install -d -m 0700 -o root -g root "$ROLLBACK_DIR/db"
  if [[ -f "$DATA_DIR/myserver.db" ]]; then
    HAD_DB=1
    for f in myserver.db myserver.db-wal myserver.db-shm; do
      if [[ -f "$DATA_DIR/$f" ]]; then cp -p -- "$DATA_DIR/$f" "$ROLLBACK_DIR/db/$f"; fi
    done
    log "Güncelleme öncesi veritabanı kopyası alındı"
  fi
}

wait_http() {
  local waited=0
  while (( waited < START_TIMEOUT )); do
    if curl -s -o /dev/null --max-time 3 "http://${LISTEN_HOST}:${PORT}/"; then return 0; fi
    if systemctl is-failed --quiet "$SERVICE"; then return 1; fi
    sleep 2
    waited=$((waited + 2))
  done
  return 1
}

rollback() {
  local reason=$1 f
  ROLLING_BACK=1
  trap - ERR
  set +e
  warn "Geri dönülüyor: $reason"
  systemctl stop "$SERVICE" >/dev/null 2>&1
  if [[ -f "$ROLLBACK_DIR/myserver" && -f "$ROLLBACK_DIR/myserver-helper" ]]; then
    install_file "$ROLLBACK_DIR/myserver" "$BIN_PATH" 0755 root root
    install_file "$ROLLBACK_DIR/myserver-helper" "$HELPER_PATH" 0755 root root
    if [[ -f "$ROLLBACK_DIR/myserver.service" ]]; then
      install_file "$ROLLBACK_DIR/myserver.service" "$UNIT_PATH" 0644 root root
      systemctl daemon-reload
    fi
  else
    warn "Saklanmış önceki sürüm bulunamadı; dosyalar geri yüklenemedi."
  fi
  if (( HAD_DB )) && [[ -f "$ROLLBACK_DIR/db/myserver.db" ]]; then
    rm -f -- "$DATA_DIR/myserver.db" "$DATA_DIR/myserver.db-wal" "$DATA_DIR/myserver.db-shm"
    for f in myserver.db myserver.db-wal myserver.db-shm; do
      if [[ -f "$ROLLBACK_DIR/db/$f" ]]; then
        cp -p -- "$ROLLBACK_DIR/db/$f" "$DATA_DIR/$f"
        chown "$SVC_USER":"$SVC_GROUP" "$DATA_DIR/$f"
      fi
    done
    warn "Veritabanı güncelleme öncesindeki haline döndürüldü."
  fi
  systemctl reset-failed "$SERVICE" >/dev/null 2>&1
  if (( WAS_ACTIVE )); then
    systemctl start "$SERVICE"
    if wait_http; then
      warn "Önceki sürüm (${OLD_VERSION:-bilinmiyor}) yeniden çalışıyor."
      write_status rolled_back "Güncelleme başarısız oldu ($reason). Önceki sürüme geri dönüldü."
    else
      warn "Geri dönüşten sonra da panel yanıt vermiyor. Günlükler: journalctl -u $SERVICE -e"
      journalctl -u "$SERVICE" -n 40 --no-pager >&2
      write_status failed "Güncelleme başarısız oldu ve geri dönüşten sonra panel başlatılamadı."
    fi
  else
    write_status rolled_back "Güncelleme başarısız oldu ($reason). Önceki sürüm geri yüklendi; servis güncellemeden önce de çalışmıyordu."
  fi
  cleanup
  exit 1
}

apply_update() {
  CURRENT_STEP="servisin durdurulması"
  if systemctl is-active --quiet "$SERVICE"; then WAS_ACTIVE=1; fi
  CHANGED=1
  systemctl stop "$SERVICE"
  snapshot_db

  CURRENT_STEP="dosyaların kurulması"
  install_file "$SRC_DIR/myserver-linux-$ARCH" "$BIN_PATH" 0755 root root
  install_file "$SRC_DIR/myserver-helper-linux-$ARCH" "$HELPER_PATH" 0755 root root

  local stage="$SHARE_DIR/apps.new.$$"
  rm -rf -- "$stage"
  install -d -m 0755 -o root -g root "$stage/manifests" "$stage/icons"
  if [[ -d "$SRC_DIR/apps/manifests" ]]; then cp -R -- "$SRC_DIR/apps/manifests/." "$stage/manifests/"; fi
  if [[ -d "$SRC_DIR/apps/icons" ]]; then cp -R -- "$SRC_DIR/apps/icons/." "$stage/icons/"; fi
  chown -R root:root "$stage"
  chmod -R u=rwX,go=rX "$stage"
  rm -rf -- "$SHARE_DIR/apps.old"
  if [[ -d "$SHARE_DIR/apps" ]]; then mv -- "$SHARE_DIR/apps" "$SHARE_DIR/apps.old"; fi
  mv -- "$stage" "$SHARE_DIR/apps"
  rm -rf -- "$SHARE_DIR/apps.old"

  install -d -m 0755 -o root -g root "$SHARE_DIR/scripts" "$SHARE_DIR/packaging"
  local name
  for name in install.sh update.sh uninstall.sh backup.sh; do
    if [[ -f "$SRC_DIR/scripts/$name" ]]; then
      install_file "$SRC_DIR/scripts/$name" "$SHARE_DIR/scripts/$name" 0755 root root
    fi
  done
  for name in myserver.service myserver.sudoers myserver.env; do
    if [[ -f "$SRC_DIR/packaging/$name" ]]; then
      install_file "$SRC_DIR/packaging/$name" "$SHARE_DIR/packaging/$name" 0644 root root
    fi
  done
  if [[ -f "$SRC_DIR/README.md" ]]; then
    install_file "$SRC_DIR/README.md" "$SHARE_DIR/README.md" 0644 root root
  fi
  if ! cmp -s -- "$SRC_DIR/packaging/myserver.service" "$UNIT_PATH"; then
    install_file "$SRC_DIR/packaging/myserver.service" "$UNIT_PATH" 0644 root root
    systemctl daemon-reload
    log "Servis birimi güncellendi"
  fi
  # sudoers kuralı ve /etc/myserver/myserver.env güncelleme sırasında DEĞİŞTİRİLMEZ.

  CURRENT_STEP="veritabanı güncellemesi"
  runuser -u "$SVC_USER" -- env MYSERVER_DATA_DIR="$DATA_DIR" MYSERVER_LOG_LEVEL=warn \
    "$BIN_PATH" --migrate \
    || die "Veritabanı güncellenemedi (myserver --migrate başarısız oldu)."

  CURRENT_STEP="servisin başlatılması"
  systemctl reset-failed "$SERVICE" >/dev/null 2>&1 || true
  systemctl start "$SERVICE"
  if ! wait_http; then
    journalctl -u "$SERVICE" -n 40 --no-pager >&2 || true
    die "Yeni sürüm $START_TIMEOUT saniye içinde yanıt vermedi."
  fi
  CHANGED=0

  printf '%s\n' "$NEW_VERSION" >"$SHARE_DIR/VERSION.new.$$"
  chmod 0644 "$SHARE_DIR/VERSION.new.$$"
  mv -f -- "$SHARE_DIR/VERSION.new.$$" "$SHARE_DIR/VERSION"
}

main() {
  parse_args "$@"
  [[ ${EUID:-$(id -u)} -eq 0 ]] || die "Bu betik root yetkisiyle çalıştırılmalıdır."
  detach_if_needed

  # Aynı anda yalnızca bir güncelleme.
  exec 9>"$LOCK_FILE"
  if ! flock -n 9; then
    STATUS_FILE=""
    die "Bir MyServer güncellemesi zaten çalışıyor."
  fi

  preflight
  write_status running "Güncelleme başladı."
  log "MyServer güncellemesi başlıyor (${VERSION_ARG:-$FROM_PATH})"
  obtain_source
  probe_new
  save_previous
  apply_update

  CURRENT_STEP="tamamlandı"
  write_status success "MyServer $NEW_VERSION sürümüne güncellendi."
  log "Güncelleme tamamlandı: ${OLD_VERSION:-?} -> $NEW_VERSION"
  log "Geri dönüş dosyaları: $ROLLBACK_DIR"
}

main "$@"
