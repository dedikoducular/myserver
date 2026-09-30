#!/usr/bin/env bash
# MyServer kurulum betiği.
#
# Kullanım:
#   Yerel kip   : sudo ./scripts/install.sh [seçenekler]
#   İndirme kipi: curl -fsSL https://raw.githubusercontent.com/dedikoducular/myserver/main/scripts/install.sh | sudo bash -s -- [seçenekler]
#
# Betik tekrar çalıştırılabilir: mevcut kurulumu yerinde yükseltir, verileri ve
# yapılandırmayı korur.

set -Eeuo pipefail

# ---------------------------------------------------------------------------
# YAYIN ADRESİ - proje sahibi, indirme alanı hazır olduğunda yalnızca bu satırı
# doldurur (örnek biçim: https://indir.ornek-alan.tld/myserver). Boş olduğu
# sürece indirme kipi için MYSERVER_RELEASE_URL ortam değişkeni veya
# --release-url seçeneği verilmelidir.
# Beklenen düzen: <adres>/<sürüm>/myserver-linux-<mimari>.tar.gz
#                 <adres>/<sürüm>/SHA256SUMS         (<sürüm>: "latest" veya "v1.2.3")
# ---------------------------------------------------------------------------
MYSERVER_DEFAULT_RELEASE_URL="https://github.com/dedikoducular/myserver/releases"

# --- Sabit yollar (kodun diğer bölümleri bunlara bağlıdır) -----------------
readonly BIN_PATH="/usr/local/bin/myserver"
readonly HELPER_DIR="/usr/local/libexec"
readonly HELPER_PATH="/usr/local/libexec/myserver-helper"
readonly SHARE_DIR="/usr/local/share/myserver"
readonly ROLLBACK_DIR="/usr/local/share/myserver/rollback"
readonly DATA_DIR_DEFAULT="/var/lib/myserver"
readonly CONF_DIR="/etc/myserver"
readonly ENV_FILE="/etc/myserver/myserver.env"
readonly STATE_FILE="/etc/myserver/install.state"
readonly SUDOERS_PATH="/etc/sudoers.d/myserver"
readonly UNIT_PATH="/etc/systemd/system/myserver.service"
readonly SVC_USER="myserver"
readonly SVC_GROUP="myserver"
readonly SERVICE="myserver"
readonly DEFAULT_PORT="8080"
readonly UFW_COMMENT="MyServer panel"
readonly TOTAL_STEPS=16

# --- Seçenekler --------------------------------------------------------------
OPT_PORT=""
OPT_FORCE=0
OPT_NO_DOCKER=0
OPT_YES=0
OPT_PUBLIC=0
OPT_RELEASE_URL="${MYSERVER_RELEASE_URL:-}"
OPT_VERSION="${MYSERVER_VERSION:-latest}"

# --- Çalışma durumu ----------------------------------------------------------
CURRENT_STEP="başlangıç"
WORK_DIR=""
SRC_DIR=""
ARCH=""
OS_ID=""
OS_VERSION_ID=""
OS_CODENAME=""
OS_LIKE=""
INTERNET_OK=0
PORT=""
CURRENT_PORT=""
LISTEN_HOST=""
DATA_DIR="$DATA_DIR_DEFAULT"
LAN_IP=""
LAN_DEV=""
LAN_SUBNET=""
IS_UPGRADE=0
NOTES=()

# --- Çıktı yardımcıları ------------------------------------------------------
if [[ -t 1 ]]; then
  C_RED=$'\033[31m'; C_GREEN=$'\033[32m'; C_YELLOW=$'\033[33m'; C_BLUE=$'\033[36m'; C_BOLD=$'\033[1m'; C_OFF=$'\033[0m'
else
  C_RED=""; C_GREEN=""; C_YELLOW=""; C_BLUE=""; C_BOLD=""; C_OFF=""
fi

step() {
  CURRENT_STEP="$2"
  printf '\n%s[%s/%s]%s %s%s%s\n' "$C_BLUE" "$1" "$TOTAL_STEPS" "$C_OFF" "$C_BOLD" "$2" "$C_OFF"
}
info() { printf '    %s\n' "$*"; }
ok()   { printf '    %s✓%s %s\n' "$C_GREEN" "$C_OFF" "$*"; }
warn() { printf '    %sUYARI:%s %s\n' "$C_YELLOW" "$C_OFF" "$*" >&2; }
note() { NOTES+=("$*"); }
die()  {
  trap - ERR
  printf '\n%sHATA:%s %s\n' "$C_RED" "$C_OFF" "$*" >&2
  exit 1
}

on_error() {
  local code=$1 line=$2
  trap - ERR
  printf '\n%sHATA:%s Kurulum "%s" adımında, %s. satırda durdu (çıkış kodu %s).\n' \
    "$C_RED" "$C_OFF" "$CURRENT_STEP" "$line" "$code" >&2
  printf '      Kurulum yarıda kaldı; mevcut verilere dokunulmadı. Sorunu giderdikten sonra\n' >&2
  printf '      betiği yeniden çalıştırabilirsiniz (tekrar çalıştırmak güvenlidir).\n' >&2
  exit "$code"
}
cleanup() {
  if [[ -n "$WORK_DIR" && -d "$WORK_DIR" ]]; then
    rm -rf -- "$WORK_DIR"
  fi
}
trap 'on_error $? $LINENO' ERR
trap cleanup EXIT

usage() {
  cat <<'EOF'
MyServer kurulum betiği

Kullanım:
  sudo ./scripts/install.sh [seçenekler]
  curl -fsSL https://raw.githubusercontent.com/dedikoducular/myserver/main/scripts/install.sh | sudo bash -s -- [seçenekler]

Seçenekler:
  --port <numara>       Panelin dinleyeceği port (1024-65535, varsayılan 8080).
  --release-url <url>   Sürüm dosyalarının indirileceği temel https adresi
                        (ortam değişkeni: MYSERVER_RELEASE_URL).
  --version <sürüm>     İndirilecek sürüm: "latest" veya "v1.2.3"
                        (ortam değişkeni: MYSERVER_VERSION, varsayılan latest).
  --no-docker           Docker kurulumunu atla.
  --force               Desteklenmeyen işletim sisteminde de devam et.
  --allow-public        Güvenlik duvarında panel portunu TÜM adreslere aç
                        (yalnızca UFW etkinse uygulanır; önerilmez).
  --yes                 Soru sormadan devam et.
  --help                Bu yardımı göster.

Kaynak dosyalar:
  Yerel kip   : betik, "make release" çıktısı olan dist/ dizinini veya açılmış
                bir sürüm arşivini kullanır.
  İndirme kipi: yerel dosya yoksa sürüm arşivi --release-url adresinden indirilir
                ve SHA-256 özeti doğrulanır.
EOF
}

# --- Genel yardımcılar -------------------------------------------------------
have() { command -v "$1" >/dev/null 2>&1; }

tty_available() { ( : </dev/tty ) 2>/dev/null; }

confirm() {
  local answer=""
  if (( OPT_YES )); then return 0; fi
  if ! tty_available; then
    info "Etkileşimli terminal yok; soru sorulmadan devam ediliyor."
    return 0
  fi
  printf '    %s [e/H]: ' "$1" >/dev/tty
  read -r answer </dev/tty || answer=""
  [[ "$answer" =~ ^[eEyY]$ ]]
}

env_get() {
  local key=$1 value=""
  [[ -f "$ENV_FILE" ]] || return 0
  value=$(sed -n "s/^${key}=//p" "$ENV_FILE" | tail -n 1)
  value=${value%$'\r'}
  value=${value#\"}; value=${value%\"}
  value=${value#\'}; value=${value%\'}
  printf '%s' "$value"
}

env_set() {
  local key=$1 value=$2 tmp
  tmp=$(mktemp "${ENV_FILE}.XXXXXX")
  awk -v k="$key" -v v="$value" '
    index($0, k "=") == 1 { if (!done) { print k "=" v; done = 1 }; next }
    { print }
    END { if (!done) print k "=" v }
  ' "$ENV_FILE" >"$tmp"
  chown root:"$SVC_GROUP" "$tmp"
  chmod 0640 "$tmp"
  mv -f -- "$tmp" "$ENV_FILE"
}

state_get() {
  [[ -f "$STATE_FILE" ]] || return 0
  sed -n "s/^${1}=//p" "$STATE_FILE" | tail -n 1
}

state_set() {
  local key=$1 value=$2 tmp
  tmp=$(mktemp "${STATE_FILE}.XXXXXX")
  if [[ -f "$STATE_FILE" ]]; then
    grep -v "^${key}=" "$STATE_FILE" >"$tmp" || true
  fi
  if [[ -n "$value" ]]; then
    printf '%s=%s\n' "$key" "$value" >>"$tmp"
  fi
  chown root:root "$tmp"
  chmod 0600 "$tmp"
  mv -f -- "$tmp" "$STATE_FILE"
}

# Dosyayı hedefin yanına yazıp yeniden adlandırarak (atomik) kurar.
install_file() {
  local src=$1 dst=$2 mode=$3 owner=$4 group=$5 tmp
  tmp="${dst}.new.$$"
  install -m "$mode" -o "$owner" -g "$group" -- "$src" "$tmp"
  mv -f -- "$tmp" "$dst"
}

pkg_installed() {
  dpkg-query -W -f='${Status}' "$1" 2>/dev/null | grep -q '^install ok installed$'
}

apt_update_once() {
  if [[ -z "${APT_UPDATED:-}" ]]; then
    (( INTERNET_OK )) || die "Paket listesi güncellenemiyor: internet bağlantısı yok."
    info "Paket listesi güncelleniyor (apt-get update)..."
    DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=180 update -qq
    APT_UPDATED=1
  fi
}

apt_install() {
  DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=180 \
    install -y -qq --no-install-recommends "$@"
}

is_private_ipv4() {
  local ip=$1 a b
  [[ "$ip" =~ ^([0-9]{1,3})\.([0-9]{1,3})\.[0-9]{1,3}\.[0-9]{1,3}(/[0-9]{1,2})?$ ]] || return 1
  a=${BASH_REMATCH[1]}; b=${BASH_REMATCH[2]}
  if (( a == 10 )); then return 0; fi
  if (( a == 172 && b >= 16 && b <= 31 )); then return 0; fi
  if (( a == 192 && b == 168 )); then return 0; fi
  return 1
}

detect_lan() {
  local route="" line=""
  LAN_IP=""; LAN_DEV=""; LAN_SUBNET=""
  have ip || return 0
  route=$(ip -4 route get 1.1.1.1 2>/dev/null | head -n 1 || true)
  if [[ "$route" =~ dev[[:space:]]+([^[:space:]]+) ]]; then LAN_DEV=${BASH_REMATCH[1]}; fi
  if [[ "$route" =~ src[[:space:]]+([0-9.]+) ]]; then LAN_IP=${BASH_REMATCH[1]}; fi
  if [[ -z "$LAN_IP" ]] && have hostname; then
    LAN_IP=$(hostname -I 2>/dev/null | awk '{print $1}' || true)
  fi
  if [[ -n "$LAN_DEV" && -n "$LAN_IP" ]]; then
    while IFS= read -r line; do
      if [[ "$line" =~ ^([0-9.]+/[0-9]+)[[:space:]] && "$line" == *"src $LAN_IP"* ]]; then
        LAN_SUBNET=${BASH_REMATCH[1]}
        break
      fi
    done < <(ip -4 route show dev "$LAN_DEV" scope link 2>/dev/null || true)
  fi
}

# --- Bağımsız değişkenler ----------------------------------------------------
parse_args() {
  local port_given=0
  while (( $# )); do
    case "$1" in
      --port)          [[ $# -ge 2 ]] || die "--port bir değer gerektirir."; OPT_PORT=$2; port_given=1; shift 2 ;;
      --port=*)        OPT_PORT=${1#*=}; port_given=1; shift ;;
      --release-url)   [[ $# -ge 2 ]] || die "--release-url bir değer gerektirir."; OPT_RELEASE_URL=$2; shift 2 ;;
      --release-url=*) OPT_RELEASE_URL=${1#*=}; shift ;;
      --version)       [[ $# -ge 2 ]] || die "--version bir değer gerektirir."; OPT_VERSION=$2; shift 2 ;;
      --version=*)     OPT_VERSION=${1#*=}; shift ;;
      --force)         OPT_FORCE=1; shift ;;
      --no-docker)     OPT_NO_DOCKER=1; shift ;;
      --allow-public)  OPT_PUBLIC=1; shift ;;
      --yes|-y)        OPT_YES=1; shift ;;
      --help|-h)       usage; exit 0 ;;
      *)               usage >&2; die "Bilinmeyen seçenek: $1" ;;
    esac
  done

  if [[ -z "$OPT_RELEASE_URL" ]]; then OPT_RELEASE_URL=$MYSERVER_DEFAULT_RELEASE_URL; fi
  OPT_RELEASE_URL=${OPT_RELEASE_URL%/}
  if [[ -n "$OPT_RELEASE_URL" && ! "$OPT_RELEASE_URL" =~ ^https://[A-Za-z0-9._:/%+~-]+$ ]]; then
    die "Yayın adresi geçersiz. Yalnızca https:// ile başlayan bir adres kabul edilir."
  fi
  if [[ ! "$OPT_VERSION" =~ ^(latest|v?[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}(-[0-9A-Za-z][0-9A-Za-z.]{0,30})?)$ ]]; then
    die "Sürüm geçersiz: \"$OPT_VERSION\". Örnek: latest veya v1.2.3"
  fi
  if [[ "$OPT_VERSION" != latest && "$OPT_VERSION" != v* ]]; then OPT_VERSION="v$OPT_VERSION"; fi
  if (( port_given )); then
    [[ "$OPT_PORT" =~ ^[0-9]{1,5}$ ]] || die "Port geçersiz: \"$OPT_PORT\"."
    OPT_PORT=$((10#$OPT_PORT))
    if (( OPT_PORT < 1024 || OPT_PORT > 65535 )); then
      die "Port 1024 ile 65535 arasında olmalıdır (servis root olarak çalışmaz)."
    fi
  fi
}

# --- Adımlar -----------------------------------------------------------------
step_root() {
  step 1 "Yetki denetimi"
  if [[ ${EUID:-$(id -u)} -ne 0 ]]; then
    die "Bu betik root yetkisiyle çalıştırılmalıdır. Örnek: sudo ./scripts/install.sh"
  fi
  if ! have systemctl || [[ ! -d /run/systemd/system ]]; then
    die "systemd bulunamadı. MyServer bir systemd servisi olarak çalışır."
  fi
  if ! have apt-get || ! have dpkg-query; then
    die "apt bulunamadı. MyServer yalnızca Ubuntu/Debian tabanlı sistemlere kurulabilir."
  fi
  ok "root yetkisi ve systemd mevcut"
}

# SC1091: /etc/os-release hedef sistemde okunur; shellcheck bu dosyayı izleyemez.
# shellcheck disable=SC1091
step_os() {
  step 2 "İşletim sistemi denetimi"
  [[ -r /etc/os-release ]] || die "/etc/os-release okunamadı; işletim sistemi belirlenemiyor."
  OS_ID=$(. /etc/os-release && printf '%s' "${ID:-}")
  OS_VERSION_ID=$(. /etc/os-release && printf '%s' "${VERSION_ID:-}")
  OS_CODENAME=$(. /etc/os-release && printf '%s' "${UBUNTU_CODENAME:-${VERSION_CODENAME:-}}")
  OS_LIKE=$(. /etc/os-release && printf '%s' "${ID_LIKE:-}")
  local pretty supported=0 major="" minor=""
  pretty=$(. /etc/os-release && printf '%s' "${PRETTY_NAME:-$OS_ID $OS_VERSION_ID}")

  case "$OS_ID" in
    ubuntu)
      major=${OS_VERSION_ID%%.*}; minor=${OS_VERSION_ID#*.}
      if [[ "$OS_VERSION_ID" == "24.04" ]]; then
        supported=1
      elif [[ "$major" =~ ^[0-9]+$ && "$minor" == "04" ]] && (( major >= 22 && major % 2 == 0 )); then
        supported=1
        warn "$pretty algılandı. Birincil hedef Ubuntu Server 24.04'tür; bu sürüm daha az sınanmıştır."
      fi
      ;;
    debian)
      major=${OS_VERSION_ID%%.*}
      if [[ "$major" =~ ^[0-9]+$ ]] && (( major >= 12 )); then
        supported=1
        warn "$pretty algılandı. Birincil hedef Ubuntu Server 24.04'tür; Debian desteği daha az sınanmıştır."
      fi
      ;;
  esac

  if (( ! supported )); then
    if (( OPT_FORCE )); then
      warn "$pretty desteklenmiyor; --force verildiği için devam ediliyor. Sorun çıkabilir."
    else
      die "$pretty desteklenmiyor. Desteklenenler: Ubuntu 24.04 (birincil), diğer Ubuntu LTS sürümleri (22.04+), Debian 12+. Yine de denemek için --force kullanın."
    fi
  else
    ok "$pretty"
  fi
}

step_arch() {
  step 3 "Mimari denetimi"
  local machine
  machine=$(uname -m)
  case "$machine" in
    x86_64|amd64)  ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) die "Desteklenmeyen işlemci mimarisi: $machine. Desteklenenler: amd64 (x86_64) ve arm64 (aarch64)." ;;
  esac
  ok "$machine ($ARCH)"
}

step_internet() {
  step 4 "İnternet bağlantısı denetimi"
  local host
  INTERNET_OK=0
  for host in download.docker.com archive.ubuntu.com deb.debian.org; do
    if timeout 8 bash -c "exec 3<>/dev/tcp/${host}/443" 2>/dev/null; then
      INTERNET_OK=1
      break
    fi
  done
  if (( INTERNET_OK )); then
    ok "İnternet bağlantısı var"
  else
    warn "İnternete ulaşılamadı. Eksik paket, Docker kurulumu veya indirme gerekirse kurulum duracaktır."
  fi
}

step_deps() {
  step 5 "Bağımlılıklar"
  # Yalnızca eksik olanlar kurulur; sistem genelinde yükseltme YAPILMAZ.
  local wanted=(curl ca-certificates sudo smartmontools ufw util-linux tar unzip zip iproute2 coreutils passwd)
  local missing=() pkg
  for pkg in "${wanted[@]}"; do
    pkg_installed "$pkg" || missing+=("$pkg")
  done
  if (( ${#missing[@]} == 0 )); then
    ok "Gerekli paketlerin tümü zaten kurulu"
    return 0
  fi
  info "Eksik paketler: ${missing[*]}"
  (( INTERNET_OK )) || die "Eksik paketler var ancak internet bağlantısı yok: ${missing[*]}"
  local ufw_was_missing=0
  pkg_installed ufw || ufw_was_missing=1
  apt_update_once
  apt_install "${missing[@]}"
  if (( ufw_was_missing )); then
    info "UFW paketi kuruldu ancak ETKİNLEŞTİRİLMEDİ (güvenlik duvarı durumu değiştirilmez)."
  fi
  ok "Eksik paketler kuruldu"
}

docker_present() {
  if have docker || have dockerd; then return 0; fi
  if [[ -S /var/run/docker.sock ]]; then return 0; fi
  if systemctl cat docker.service >/dev/null 2>&1; then return 0; fi
  return 1
}

step_docker() {
  step 6 "Docker"
  if docker_present; then
    ok "Docker zaten kurulu; olduğu gibi kullanılacak (yeniden kurulmaz, ayarı değiştirilmez)"
    if have docker; then
      if ! docker compose version >/dev/null 2>&1; then
        warn "Docker Compose eklentisi bulunamadı. Uygulama kurulumu için \"docker-compose-plugin\" paketini kurmanız gerekir."
        note "Docker Compose eklentisi eksik: uygulama mağazası çalışmayabilir (docker-compose-plugin)."
      fi
      if ! docker info >/dev/null 2>&1; then
        warn "Docker servisi şu anda yanıt vermiyor. Panel çalışır, ancak Docker bölümü servis başlatılana kadar kullanılamaz."
        note "Docker servisi çalışmıyor: sudo systemctl start docker"
      fi
    fi
    return 0
  fi

  if (( OPT_NO_DOCKER )); then
    warn "Docker kurulu değil ve --no-docker verildi. Panelin Docker ve uygulama bölümleri Docker kurulana kadar çalışmaz."
    note "Docker kurulmadı (--no-docker). Daha sonra kurmak için betiği --no-docker olmadan yeniden çalıştırın."
    return 0
  fi

  (( INTERNET_OK )) || die "Docker kurulumu için internet bağlantısı gerekir. Docker olmadan devam etmek için --no-docker kullanın."

  local distro=""
  case "$OS_ID" in
    ubuntu) distro="ubuntu" ;;
    debian) distro="debian" ;;
    *)
      if [[ " $OS_LIKE " == *" ubuntu "* ]]; then distro="ubuntu"
      elif [[ " $OS_LIKE " == *" debian "* ]]; then distro="debian"
      fi
      ;;
  esac
  [[ -n "$distro" && "$OS_CODENAME" =~ ^[a-z]+$ ]] \
    || die "Bu dağıtım için Docker deposu belirlenemedi. Docker'ı elle kurun veya --no-docker kullanın."

  info "Docker Engine, Docker'ın resmi apt deposundan kuruluyor ($distro $OS_CODENAME)..."
  install -m 0755 -d /etc/apt/keyrings
  local key_tmp
  key_tmp=$(mktemp /etc/apt/keyrings/docker.asc.XXXXXX)
  if ! curl -fsSL --proto '=https' --tlsv1.2 "https://download.docker.com/linux/${distro}/gpg" -o "$key_tmp"; then
    rm -f -- "$key_tmp"
    die "Docker imza anahtarı indirilemedi."
  fi
  if ! grep -q 'BEGIN PGP PUBLIC KEY BLOCK' "$key_tmp"; then
    rm -f -- "$key_tmp"
    die "İndirilen Docker imza anahtarı geçerli görünmüyor."
  fi
  chmod 0644 "$key_tmp"
  mv -f -- "$key_tmp" /etc/apt/keyrings/docker.asc

  local list_tmp
  list_tmp=$(mktemp /etc/apt/sources.list.d/docker.list.XXXXXX)
  printf 'deb [arch=%s signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/%s %s stable\n' \
    "$(dpkg --print-architecture)" "$distro" "$OS_CODENAME" >"$list_tmp"
  chmod 0644 "$list_tmp"
  mv -f -- "$list_tmp" /etc/apt/sources.list.d/docker.list

  APT_UPDATED=""
  apt_update_once
  apt_install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
  systemctl enable --now docker.service >/dev/null
  DOCKER_INSTALLED_NOW=1
  ok "Docker Engine ve Compose eklentisi kuruldu (TCP soketi etkinleştirilmedi; yalnızca yerel unix soketi)"
}

step_user() {
  step 7 "Sistem kullanıcısı"
  if ! getent group "$SVC_GROUP" >/dev/null; then
    groupadd --system "$SVC_GROUP"
    ok "\"$SVC_GROUP\" grubu oluşturuldu"
  fi
  if ! getent passwd "$SVC_USER" >/dev/null; then
    useradd --system --gid "$SVC_GROUP" --home-dir "$DATA_DIR_DEFAULT" --no-create-home \
      --shell /usr/sbin/nologin --comment "MyServer" "$SVC_USER"
    ok "\"$SVC_USER\" sistem kullanıcısı oluşturuldu (oturum açamaz)"
  else
    ok "\"$SVC_USER\" kullanıcısı zaten var"
  fi
  if ! getent group docker >/dev/null; then
    # Servis birimi SupplementaryGroups=docker kullanır; grup yoksa servis başlamaz.
    groupadd --system docker
    warn "\"docker\" grubu yoktu, oluşturuldu. Docker kurulduğunda soket bu gruba ait olmalıdır."
  fi
  if ! id -nG "$SVC_USER" | tr ' ' '\n' | grep -qx docker; then
    usermod -aG docker "$SVC_USER"
    ok "\"$SVC_USER\" kullanıcısı \"docker\" grubuna eklendi"
  fi
  info "Not: docker grubu üyeliği host üzerinde root yetkisine eşdeğerdir (README: Güvenlik modeli)."
}

step_dirs() {
  step 8 "Dizinler"
  local custom
  install -d -m 0750 -o root -g "$SVC_GROUP" "$CONF_DIR"
  custom=$(env_get MYSERVER_DATA_DIR)
  if [[ -n "$custom" && "$custom" == /* ]]; then DATA_DIR=$custom; fi
  if [[ "$DATA_DIR" != "$DATA_DIR_DEFAULT" ]]; then
    info "Yapılandırmadaki özel veri dizini kullanılıyor: $DATA_DIR"
  fi
  install -d -m 0750 -o "$SVC_USER" -g "$SVC_GROUP" "$DATA_DIR"
  install -d -m 0750 -o "$SVC_USER" -g "$SVC_GROUP" \
    "$DATA_DIR/backups" "$DATA_DIR/backups/panel" "$DATA_DIR/apps" "$DATA_DIR/tmp"
  install -d -m 0755 -o root -g root "$HELPER_DIR" "$SHARE_DIR" "$SHARE_DIR/apps" \
    "$SHARE_DIR/scripts" "$SHARE_DIR/packaging"
  install -d -m 0700 -o root -g root "$ROLLBACK_DIR"

  if [[ ! -f "$ENV_FILE" ]]; then
    install_file "$SRC_DIR/packaging/myserver.env" "$ENV_FILE" 0640 root "$SVC_GROUP"
    env_set MYSERVER_LISTEN ":${PORT}"
    ok "Ortam dosyası oluşturuldu: $ENV_FILE"
  else
    chown root:"$SVC_GROUP" "$ENV_FILE"
    chmod 0640 "$ENV_FILE"
    if [[ -n "$OPT_PORT" ]]; then
      env_set MYSERVER_LISTEN "${LISTEN_HOST}:${PORT}"
      ok "Port güncellendi: $PORT"
    else
      ok "Mevcut ortam dosyası korundu: $ENV_FILE"
    fi
  fi

  if [[ -n "$OPT_RELEASE_URL" ]]; then
    env_set MYSERVER_RELEASE_URL "$OPT_RELEASE_URL"
  fi

  # Web terminalinin varsayılan kullanıcısı: kurulumu başlatan gerçek kullanıcı.
  local invoker=${SUDO_USER:-}
  if [[ -z "$(env_get MYSERVER_TERMINAL_DEFAULT_USER)" \
        && "$invoker" =~ ^[a-z_][a-z0-9_-]{0,31}$ && "$invoker" != root && "$invoker" != "$SVC_USER" ]]; then
    local invoker_uid
    invoker_uid=$(id -u "$invoker" 2>/dev/null || true)
    if [[ -n "$invoker_uid" && "$invoker_uid" != 0 ]]; then
      env_set MYSERVER_TERMINAL_DEFAULT_USER "$invoker"
      ok "Web terminali varsayılan kullanıcısı: $invoker"
    fi
  fi
  ok "Dizinler hazır"
}

step_binaries() {
  step 9 "Panel ve yardımcı program"
  local src_bin="$SRC_DIR/myserver-linux-$ARCH" src_helper="$SRC_DIR/myserver-helper-linux-$ARCH"
  local probe="$WORK_DIR/probe" new_version

  # Kurmadan önce ikili dosyaların bu makinede çalıştığını doğrula.
  install -m 0755 -- "$src_bin" "$probe"
  if ! new_version=$("$probe" --version 2>/dev/null) || [[ -z "$new_version" ]]; then
    die "Panel dosyası bu sistemde çalıştırılamadı ($src_bin). Dosya bozuk veya mimari uyumsuz olabilir."
  fi
  install -m 0755 -- "$src_helper" "$probe-helper"
  if ! "$probe-helper" list-actions >/dev/null 2>&1; then
    die "Yardımcı program bu sistemde çalıştırılamadı ($src_helper)."
  fi
  NEW_VERSION=$new_version

  if [[ -x "$BIN_PATH" && -x "$HELPER_PATH" ]]; then
    # Geri dönüş için önceki sürümün kopyası.
    install -m 0755 -o root -g root -- "$BIN_PATH" "$ROLLBACK_DIR/myserver"
    install -m 0755 -o root -g root -- "$HELPER_PATH" "$ROLLBACK_DIR/myserver-helper"
    "$BIN_PATH" --version >"$ROLLBACK_DIR/VERSION" 2>/dev/null || true
    info "Önceki sürüm geri dönüş için saklandı: $ROLLBACK_DIR"
  fi

  install_file "$src_bin" "$BIN_PATH" 0755 root root
  install_file "$src_helper" "$HELPER_PATH" 0755 root root
  ok "Kuruldu: $BIN_PATH"
  ok "Kuruldu: $HELPER_PATH (root:root 0755, setuid değil)"

  # Uygulama tanımları ve simgeler: yeni kopya hazırlanır, sonra yer değiştirilir.
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
  ok "Uygulama tanımları kuruldu: $SHARE_DIR/apps"

  local name
  for name in install.sh update.sh uninstall.sh backup.sh; do
    if [[ -f "$SRC_DIR/scripts/$name" ]]; then
      install_file "$SRC_DIR/scripts/$name" "$SHARE_DIR/scripts/$name" 0755 root root
    fi
  done
  for name in myserver.service myserver.sudoers myserver.env; do
    install_file "$SRC_DIR/packaging/$name" "$SHARE_DIR/packaging/$name" 0644 root root
  done
  if [[ -f "$SRC_DIR/README.md" ]]; then
    install_file "$SRC_DIR/README.md" "$SHARE_DIR/README.md" 0644 root root
  fi
  printf '%s\n' "$NEW_VERSION" >"$SHARE_DIR/VERSION.new.$$"
  chmod 0644 "$SHARE_DIR/VERSION.new.$$"
  mv -f -- "$SHARE_DIR/VERSION.new.$$" "$SHARE_DIR/VERSION"
  ok "Betikler kuruldu: $SHARE_DIR/scripts"

  # sudoers: geçici dosyaya yaz, visudo ile doğrula, sonra atomik olarak kur.
  have visudo || die "visudo bulunamadı (sudo paketi eksik)."
  local sudoers_tmp
  sudoers_tmp=$(mktemp /etc/sudoers.d/.myserver.XXXXXX)
  install -m 0440 -o root -g root -- "$SRC_DIR/packaging/myserver.sudoers" "$sudoers_tmp"
  if ! visudo -cf "$sudoers_tmp" >/dev/null 2>&1; then
    rm -f -- "$sudoers_tmp"
    die "sudoers dosyası doğrulanamadı; sisteme KURULMADI (mevcut sudo ayarlarınıza dokunulmadı)."
  fi
  mv -f -- "$sudoers_tmp" "$SUDOERS_PATH"
  if ! grep -Eq '^[#@]includedir[[:space:]]+/etc/sudoers\.d' /etc/sudoers 2>/dev/null; then
    warn "/etc/sudoers içinde \"@includedir /etc/sudoers.d\" satırı bulunamadı; yardımcı çalışmayabilir."
    note "/etc/sudoers dosyasında \"@includedir /etc/sudoers.d\" satırı yok: root yardımcısı çalışmaz."
  fi
  ok "sudoers kuralı doğrulandı ve kuruldu: $SUDOERS_PATH (0440)"
}

step_frontend() {
  step 10 "Web arayüzü"
  local reported
  reported=$("$BIN_PATH" --version 2>/dev/null) || die "Kurulan panel sürüm bilgisi vermedi: $BIN_PATH --version"
  [[ -n "$reported" ]] || die "Kurulan panel boş sürüm bilgisi döndürdü."
  ok "Arayüz panel dosyasına gömülüdür; ayrıca kurulacak bir şey yok (sürüm: $reported)"
}

step_service() {
  step 11 "systemd servisi"
  install_file "$SRC_DIR/packaging/myserver.service" "$UNIT_PATH" 0644 root root
  systemctl daemon-reload
  if have systemd-analyze; then
    if ! systemd-analyze verify "$UNIT_PATH" >/dev/null 2>&1; then
      warn "systemd-analyze birim dosyasında uyarı verdi: systemd-analyze verify $UNIT_PATH"
    fi
  fi
  ok "Servis birimi kuruldu: $UNIT_PATH"
}

ufw_active() {
  have ufw && LC_ALL=C ufw status 2>/dev/null | head -n 1 | grep -qi '^Status: active'
}

# Kuralın UFW'de gerçekten tanımlı olup olmadığını denetler ($1: kaynak, $2: port).
ufw_rule_exists() {
  local from=$1 port=$2 want
  if [[ "$from" == any ]]; then
    want="ufw allow ${port}/tcp"
  else
    want="ufw allow from ${from} to any port ${port} proto tcp"
  fi
  LC_ALL=C ufw show added 2>/dev/null | sed "s/ comment '.*'\$//" | grep -Fxq -- "$want"
}

ufw_remove_recorded_rule() {
  local old_port old_from
  old_port=$(state_get UFW_RULE_PORT)
  old_from=$(state_get UFW_RULE_FROM)
  [[ -n "$old_port" && -n "$old_from" ]] || return 0
  if [[ "$old_from" == any ]]; then
    ufw delete allow "${old_port}/tcp" >/dev/null 2>&1 || true
  else
    ufw delete allow from "$old_from" to any port "$old_port" proto tcp >/dev/null 2>&1 || true
  fi
  state_set UFW_RULE_PORT ""
  state_set UFW_RULE_FROM ""
}

step_firewall() {
  step 12 "Güvenlik duvarı"
  detect_lan
  if ! have ufw; then
    info "UFW kurulu değil; güvenlik duvarına dokunulmadı."
    return 0
  fi
  if ! ufw_active; then
    info "UFW kurulu ancak ETKİN DEĞİL. Durumu değiştirilmedi ve hiçbir kural eklenmedi."
    info "Daha sonra panelin güvenlik duvarı bölümünden, SSH erişimi korunarak etkinleştirebilirsiniz."
    info "Elle etkinleştirecekseniz ÖNCE SSH'a izin verin: sudo ufw allow OpenSSH && sudo ufw enable"
    note "UFW etkin değil ve etkinleştirilmedi. Panelin güvenlik duvarı bölümünden etkinleştirebilirsiniz."
    return 0
  fi

  local want_from=""
  if (( OPT_PUBLIC )); then
    want_from="any"
  elif [[ -n "$LAN_SUBNET" ]] && is_private_ipv4 "$LAN_SUBNET"; then
    want_from="$LAN_SUBNET"
  fi

  if [[ -z "$want_from" ]]; then
    warn "Özel (yerel ağ) bir alt ağ algılanamadı; güvenlik duvarına kural EKLENMEDİ."
    warn "UFW etkin olduğu için panel portu ($PORT) şu anda kapalı olabilir."
    info "Yalnızca kendi ağınıza açmak için: sudo ufw allow from <ALT_AĞ> to any port $PORT proto tcp"
    note "UFW etkin ve panel portu için kural eklenmedi (yerel alt ağ algılanamadı). Port $PORT kapalı olabilir."
    return 0
  fi

  if [[ "$(state_get UFW_RULE_PORT)" == "$PORT" && "$(state_get UFW_RULE_FROM)" == "$want_from" ]] \
     && ufw_rule_exists "$want_from" "$PORT"; then
    ok "Güvenlik duvarı kuralı zaten var ($want_from -> $PORT/tcp)"
    return 0
  fi
  ufw_remove_recorded_rule

  # Aynı kuralı yönetici kendisi eklemişse sahiplenilmez: kayda geçirilmez ve
  # kaldırma sırasında silinmez.
  if ufw_rule_exists "$want_from" "$PORT"; then
    ok "Aynı güvenlik duvarı kuralı zaten tanımlı ($want_from -> $PORT/tcp); yeni kural eklenmedi"
    info "Bu kuralı kurulum betiği eklemedi; kaldırma sırasında silinmeyecek."
    return 0
  fi

  if [[ "$want_from" == any ]]; then
    ufw allow "${PORT}/tcp" comment "$UFW_COMMENT" >/dev/null
    warn "--allow-public verildi: $PORT/tcp portu TÜM adreslere açıldı."
    note "Güvenlik duvarı: $PORT/tcp tüm adreslere açık (--allow-public). Paneli internete doğrudan açmanız önerilmez."
  else
    ufw allow from "$want_from" to any port "$PORT" proto tcp comment "$UFW_COMMENT" >/dev/null
    ok "UFW kuralı eklendi: yalnızca $want_from alt ağından $PORT/tcp erişimine izin verildi"
    note "Güvenlik duvarı: UFW'ye \"$want_from -> $PORT/tcp\" izin kuralı eklendi (yalnızca yerel ağ)."
  fi
  state_set UFW_RULE_PORT "$PORT"
  state_set UFW_RULE_FROM "$want_from"
  info "SSH, SMB ve NFS kurallarına dokunulmadı."
}

step_database() {
  step 13 "Veritabanı"
  if systemctl is-active --quiet "$SERVICE"; then
    info "Servis, veritabanı güncellemesi için durduruluyor..."
    systemctl stop "$SERVICE"
  fi
  # Ana dizin ve veritabanı dosyaları servis kullanıcısına ait olmalı.
  local f
  for f in "$DATA_DIR/myserver.db" "$DATA_DIR/myserver.db-wal" "$DATA_DIR/myserver.db-shm"; do
    if [[ -f "$f" ]]; then chown "$SVC_USER":"$SVC_GROUP" "$f"; chmod 0600 "$f"; fi
  done
  if ! runuser -u "$SVC_USER" -- env \
      MYSERVER_DATA_DIR="$DATA_DIR" MYSERVER_LOG_LEVEL=warn \
      "$BIN_PATH" --migrate; then
    die "Veritabanı hazırlanamadı (myserver --migrate başarısız oldu)."
  fi
  ok "Veritabanı hazır: $DATA_DIR/myserver.db"
}

step_permissions() {
  step 14 "İzinler"
  chown root:root "$BIN_PATH" "$HELPER_PATH" "$UNIT_PATH" "$SUDOERS_PATH"
  chmod 0755 "$BIN_PATH" "$HELPER_PATH"
  chmod 0644 "$UNIT_PATH"
  chmod 0440 "$SUDOERS_PATH"
  chown root:"$SVC_GROUP" "$CONF_DIR" "$ENV_FILE"
  chmod 0750 "$CONF_DIR"
  chmod 0640 "$ENV_FILE"
  if [[ -f "$STATE_FILE" ]]; then chown root:root "$STATE_FILE"; chmod 0600 "$STATE_FILE"; fi

  chown "$SVC_USER":"$SVC_GROUP" "$DATA_DIR" "$DATA_DIR/backups" "$DATA_DIR/apps" "$DATA_DIR/tmp"
  chmod 0750 "$DATA_DIR" "$DATA_DIR/backups" "$DATA_DIR/apps" "$DATA_DIR/tmp"
  # backups/ ve tmp/ panelin kendi dosyalarıdır. apps/ altı ÖZYİNELEMELİ olarak
  # değiştirilmez: uygulama kapsayıcıları verilerini kendi kullanıcılarıyla yazar.
  chown -R "$SVC_USER":"$SVC_GROUP" "$DATA_DIR/backups" "$DATA_DIR/tmp"
  # Veri dizininin kökündeki düz dosyalar (update.status vb.) da panele aittir.
  # Kaldırıp yeniden kurmada kullanıcı numarası değişmiş olabilir.
  find "$DATA_DIR" -maxdepth 1 -type f -exec chown "$SVC_USER":"$SVC_GROUP" {} +
  local f
  for f in "$DATA_DIR/myserver.db" "$DATA_DIR/myserver.db-wal" "$DATA_DIR/myserver.db-shm"; do
    if [[ -f "$f" ]]; then chown "$SVC_USER":"$SVC_GROUP" "$f"; chmod 0600 "$f"; fi
  done

  if runuser -u "$SVC_USER" -- /usr/bin/sudo -n -- "$HELPER_PATH" ping >/dev/null 2>&1; then
    ok "Root yardımcısı sudo üzerinden çalışıyor"
  else
    warn "Root yardımcısı sudo üzerinden çalıştırılamadı. Panel açılır, ancak yetki gerektiren işlemler başarısız olur."
    note "Root yardımcısı sınaması başarısız: sudo -u $SVC_USER sudo -n $HELPER_PATH ping"
  fi
  ok "Sahiplik ve izinler ayarlandı"
}

step_start() {
  step 15 "Servisin başlatılması"
  systemctl reset-failed "$SERVICE" >/dev/null 2>&1 || true
  systemctl enable "$SERVICE" >/dev/null 2>&1
  systemctl restart "$SERVICE"

  local host=${LISTEN_HOST:-127.0.0.1} waited=0 timeout_s=60
  if [[ "$host" == "0.0.0.0" || "$host" == "[::]" || "$host" == "*" ]]; then host="127.0.0.1"; fi
  info "Panelin yanıt vermesi bekleniyor (en çok ${timeout_s} sn)..."
  while (( waited < timeout_s )); do
    if curl -s -o /dev/null --max-time 3 "http://${host}:${PORT}/"; then
      ok "Panel $PORT portunda yanıt veriyor"
      return 0
    fi
    if systemctl is-failed --quiet "$SERVICE"; then break; fi
    sleep 2
    waited=$((waited + 2))
  done

  printf '\n%sHATA:%s Panel %s saniye içinde yanıt vermedi. Son günlük kayıtları:\n\n' \
    "$C_RED" "$C_OFF" "$timeout_s" >&2
  journalctl -u "$SERVICE" -n 40 --no-pager >&2 || true
  printf '\n' >&2
  die "Servis başlatılamadı. Ayrıntı için: journalctl -u $SERVICE -e"
}

step_lan_ip() {
  step 16 "Ağ adresi"
  detect_lan
  if [[ -n "$LISTEN_HOST" && "$LISTEN_HOST" != "0.0.0.0" && "$LISTEN_HOST" != "[::]" ]]; then
    LAN_IP=$LISTEN_HOST
  fi
  if [[ -z "$LAN_IP" ]]; then
    LAN_IP="SUNUCU_IP"
    warn "Yerel ağ adresi algılanamadı. \"ip -4 addr\" komutuyla adresinizi öğrenebilirsiniz."
  else
    ok "Birincil adres: $LAN_IP${LAN_DEV:+ ($LAN_DEV)}"
  fi
}

# --- Kaynak dosyaların bulunması --------------------------------------------
source_complete() {
  local dir=$1
  [[ -f "$dir/myserver-linux-$ARCH" && -f "$dir/myserver-helper-linux-$ARCH" \
     && -f "$dir/packaging/myserver.service" && -f "$dir/packaging/myserver.sudoers" \
     && -f "$dir/packaging/myserver.env" && -f "$dir/scripts/update.sh" ]]
}

# Yerel dizinde SHA256SUMS varsa ikili dosyaları ona göre doğrular.
verify_local_sums() {
  local dir=$1 name expected actual
  [[ -f "$dir/SHA256SUMS" ]] || return 0
  for name in "myserver-linux-$ARCH" "myserver-helper-linux-$ARCH"; do
    expected=$(awk -v n="$name" '{ f = $2; sub(/^\*/, "", f); if (f == n) { print $1; exit } }' "$dir/SHA256SUMS")
    [[ -n "$expected" ]] || continue
    actual=$(sha256sum -- "$dir/$name" | awk '{print $1}')
    [[ "$expected" == "$actual" ]] || die "SHA-256 doğrulaması başarısız: $dir/$name"
  done
  info "Yerel dosyaların SHA-256 özeti doğrulandı"
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

download_release() {
  local base tarball="myserver-linux-$ARCH.tar.gz"
  local expected actual entry
  base=$(release_base "$OPT_RELEASE_URL" "$OPT_VERSION")
  (( INTERNET_OK )) || die "Sürüm dosyaları indirilemiyor: internet bağlantısı yok."
  have curl || { apt_update_once; apt_install curl ca-certificates; }

  info "İndiriliyor: $base/$tarball"
  curl -fSL --proto '=https' --proto-redir '=https' --max-redirs 5 --tlsv1.2 --retry 3 --connect-timeout 20 \
    -o "$WORK_DIR/$tarball" "$base/$tarball" \
    || die "Sürüm arşivi indirilemedi: $base/$tarball"
  curl -fsSL --proto '=https' --proto-redir '=https' --max-redirs 5 --tlsv1.2 --retry 3 --connect-timeout 20 \
    -o "$WORK_DIR/SHA256SUMS" "$base/SHA256SUMS" \
    || die "Özet dosyası indirilemedi: $base/SHA256SUMS"

  expected=$(awk -v n="$tarball" '{ f = $2; sub(/^\*/, "", f); if (f == n) { print $1; exit } }' "$WORK_DIR/SHA256SUMS")
  [[ "$expected" =~ ^[0-9a-fA-F]{64}$ ]] \
    || die "SHA256SUMS dosyasında $tarball için geçerli bir özet bulunamadı."
  actual=$(sha256sum -- "$WORK_DIR/$tarball" | awk '{print $1}')
  if [[ "${expected,,}" != "${actual,,}" ]]; then
    die "SHA-256 doğrulaması BAŞARISIZ. İndirilen arşiv bozuk veya değiştirilmiş; kurulum durduruldu."
  fi
  ok "SHA-256 özeti doğrulandı"

  while IFS= read -r entry; do
    if [[ "$entry" == /* || "$entry" == *".."* ]]; then
      die "Arşiv güvenli olmayan bir yol içeriyor: $entry"
    fi
  done < <(tar -tzf "$WORK_DIR/$tarball")
  mkdir -p "$WORK_DIR/release"
  tar -xzf "$WORK_DIR/$tarball" -C "$WORK_DIR/release" --no-same-owner --no-same-permissions

  local candidate
  for candidate in "$WORK_DIR/release/myserver" "$WORK_DIR/release"; do
    if source_complete "$candidate"; then SRC_DIR=$candidate; return 0; fi
  done
  die "İndirilen arşivin içeriği beklenen düzende değil."
}

resolve_source() {
  local script_path="${BASH_SOURCE[0]:-}" script_dir="" root="" candidate
  if [[ -n "$script_path" && -f "$script_path" ]]; then
    script_dir=$(cd -- "$(dirname -- "$script_path")" && pwd -P)
    root=$(dirname -- "$script_dir")
    for candidate in "$root/dist" "$root"; do
      if source_complete "$candidate"; then
        SRC_DIR=$candidate
        info "Yerel kip: dosyalar $SRC_DIR dizininden alınacak"
        verify_local_sums "$SRC_DIR"
        return 0
      fi
    done
  fi

  if [[ -n "$OPT_RELEASE_URL" ]]; then
    info "İndirme kipi: $OPT_RELEASE_URL ($OPT_VERSION)"
    download_release
    return 0
  fi

  die "Kurulacak dosyalar bulunamadı.
      Yerel kip için: depoda \"make release\" çalıştırın ve betiği depo ya da açılmış
      sürüm dizininden başlatın (dist/myserver-linux-$ARCH ve dist/myserver-helper-linux-$ARCH
      dosyaları gerekir).
      İndirme kipi için: sürüm adresini verin, örneğin
        curl -fsSL https://raw.githubusercontent.com/dedikoducular/myserver/main/scripts/install.sh | sudo bash
      veya --release-url <adres> seçeneğini kullanın."
}

resolve_port() {
  local listen
  listen=$(env_get MYSERVER_LISTEN)
  LISTEN_HOST=""
  if [[ "$listen" =~ ^(.*):([0-9]{1,5})$ ]]; then
    LISTEN_HOST=${BASH_REMATCH[1]}
    PORT=${BASH_REMATCH[2]}
  fi
  CURRENT_PORT=$PORT
  if [[ -n "$OPT_PORT" ]]; then PORT=$OPT_PORT; fi
  if [[ -z "$PORT" ]]; then PORT=$DEFAULT_PORT; fi
}

port_in_use() {
  if have ss; then
    ss -H -ltn "sport = :$1" 2>/dev/null | grep -q .
    return
  fi
  # ss yoksa (iproute2 kurulmadan önce) bağlanmayı deneyerek denetle.
  timeout 3 bash -c "exec 3<>/dev/tcp/127.0.0.1/$1" 2>/dev/null
}

# Port başka bir programca kullanılıyorsa hiçbir şey kurmadan dur. Panel
# çalışıyorsa ve port değişmiyorsa portu dinleyen panelin kendisidir.
check_port_free() {
  if systemctl is-active --quiet "$SERVICE" 2>/dev/null && [[ "$PORT" == "$CURRENT_PORT" ]]; then
    return 0
  fi
  if port_in_use "$PORT"; then
    die "$PORT portu başka bir program tarafından kullanılıyor. Farklı bir port seçin: --port <numara>"
  fi
}

print_summary() {
  local n
  if (( ${#NOTES[@]} )); then
    printf '\n%sNotlar:%s\n' "$C_YELLOW" "$C_OFF"
    for n in "${NOTES[@]}"; do printf '  - %s\n' "$n"; done
  fi
  if (( IS_UPGRADE )); then
    printf '\nMevcut kurulum yükseltildi; veriler ve yapılandırma korundu.\n'
  else
    printf '\nİlk açılışta kurulum sihirbazı yönetici hesabını oluşturmanızı ister.\n'
  fi
  cat <<EOF

========================================
 MyServer başarıyla kuruldu
========================================

Panel:
http://${LAN_IP}:${PORT}

Service:
systemctl status myserver

Logs:
journalctl -u myserver -f
EOF
}

main() {
  parse_args "$@"

  printf '%sMyServer kurulumu%s\n' "$C_BOLD" "$C_OFF"

  step_root
  step_os
  step_arch
  step_internet

  CURRENT_STEP="hazırlık"
  # /tmp "noexec" bağlanmış olabilir; ikili dosyalar sınanacağı için /var/lib kullanılır.
  WORK_DIR=$(mktemp -d /var/lib/myserver-install.XXXXXX)
  chmod 0700 "$WORK_DIR"
  if [[ -x "$BIN_PATH" || -f "$ENV_FILE" ]]; then IS_UPGRADE=1; fi
  resolve_port
  check_port_free

  printf '\n'
  if (( IS_UPGRADE )); then
    info "Mevcut bir kurulum bulundu; yerinde yükseltilecek (veriler ve ayarlar korunur)."
  fi
  info "Panel portu: $PORT"
  confirm "Kurulum başlatılsın mı?" || die "Kurulum kullanıcı tarafından iptal edildi."

  step_deps
  # iproute2 yeni kurulduysa port denetimi artık "ss" ile kesin olarak yapılır.
  check_port_free
  step_docker

  CURRENT_STEP="kaynak dosyalar"
  resolve_source

  step_user
  step_dirs
  if [[ "${DOCKER_INSTALLED_NOW:-0}" == 1 ]]; then state_set DOCKER_INSTALLED_BY_MYSERVER 1; fi
  step_binaries
  step_frontend
  step_service
  step_firewall
  step_database
  step_permissions
  step_start
  step_lan_ip

  CURRENT_STEP="özet"
  print_summary
}

# Betik curl ile akıştan okunurken yarım indirilmiş bir dosyanın çalışmaması
# için tüm iş main() içinde ve çağrı en son satırdadır.
main "$@"
