#!/usr/bin/env bash
# MyServer kaldırma betiği.
#
# Kullanım:
#   sudo /usr/local/share/myserver/scripts/uninstall.sh            (etkileşimli)
#   sudo /usr/local/share/myserver/scripts/uninstall.sh --level 1 --yes
#
# Bu betik Docker'ı ASLA kaldırmaz, MyServer'ın oluşturmadığı kapsayıcılara ve
# birimlere ASLA dokunmaz ve açık onay olmadan kullanıcı verisi silmez.

set -Eeuo pipefail

readonly BIN_PATH="/usr/local/bin/myserver"
readonly HELPER_PATH="/usr/local/libexec/myserver-helper"
readonly SHARE_DIR="/usr/local/share/myserver"
readonly DATA_DIR="/var/lib/myserver"
# Root yardımcısının paket güncellemesi günlükleri için oluşturduğu dizin (root:root).
readonly UPDATES_DIR="/var/lib/myserver-updates"
readonly CONF_DIR="/etc/myserver"
readonly ENV_FILE="/etc/myserver/myserver.env"
readonly STATE_FILE="/etc/myserver/install.state"
readonly SUDOERS_PATH="/etc/sudoers.d/myserver"
readonly UNIT_PATH="/etc/systemd/system/myserver.service"
readonly SVC_USER="myserver"
readonly SVC_GROUP="myserver"
readonly SERVICE="myserver"
readonly LABEL="io.myserver.managed=true"
readonly WORD_DATA="KALDIR"
readonly WORD_VOLUMES="SIL"

LEVEL=""
OPT_YES=0
OPT_CONFIRM_DATA=0
OPT_REMOVE_VOLUMES=0
CURRENT_STEP="başlangıç"
REMOVE_VOLUMES=0
VOLUMES=()

if [[ -t 1 ]]; then
  C_RED=$'\033[31m'; C_GREEN=$'\033[32m'; C_YELLOW=$'\033[33m'; C_BOLD=$'\033[1m'; C_OFF=$'\033[0m'
else
  C_RED=""; C_GREEN=""; C_YELLOW=""; C_BOLD=""; C_OFF=""
fi

info() { printf '  %s\n' "$*"; }
ok()   { printf '  %s✓%s %s\n' "$C_GREEN" "$C_OFF" "$*"; }
warn() { printf '  %sUYARI:%s %s\n' "$C_YELLOW" "$C_OFF" "$*" >&2; }
die()  { trap - ERR; printf '\n%sHATA:%s %s\n' "$C_RED" "$C_OFF" "$*" >&2; exit 1; }
step() { CURRENT_STEP="$1"; printf '\n%s%s%s\n' "$C_BOLD" "$1" "$C_OFF"; }

on_error() {
  local code=$1 line=$2
  trap - ERR
  printf '\n%sHATA:%s Kaldırma "%s" adımında, %s. satırda durdu (çıkış kodu %s).\n' \
    "$C_RED" "$C_OFF" "$CURRENT_STEP" "$line" "$code" >&2
  printf '      Betiği yeniden çalıştırabilirsiniz; kalan adımlar tamamlanır.\n' >&2
  exit "$code"
}
trap 'on_error $? $LINENO' ERR

usage() {
  cat <<'EOF'
MyServer kaldırma betiği

Kullanım:
  uninstall.sh [seçenekler]

Düzeyler:
  1  Yalnızca paneli kaldır
     (program, yardımcı, servis, sudoers kuralı; /var/lib/myserver ve
      /etc/myserver KORUNUR)
  2  Panel + yapılandırma ve veriler
     (ek olarak /etc/myserver ve /var/lib/myserver SİLİNİR - panel
      veritabanı, uygulama verileri ve YEDEKLER dahil)
  3  Panel + tüm uygulamalar
     (ek olarak MyServer'ın oluşturduğu kapsayıcılar ve ağlar kaldırılır;
      Docker birimleri yalnızca ayrı bir onayla silinir)

Seçenekler:
  --level <1|2|3>        Kaldırma düzeyi (verilmezse sorulur).
  --yes                  Soru sormadan çalış (--level zorunludur).
  --confirm-data-loss    --yes ile düzey 2 ve 3 için ZORUNLU: verilerin ve
                         yedeklerin silinmesini onaylar.
  --remove-volumes       --yes ile düzey 3'te MyServer'ın oluşturduğu Docker
                         birimlerini de sil. Verilmezse birimler korunur.
  --help                 Bu yardımı göster.
EOF
}

have() { command -v "$1" >/dev/null 2>&1; }
tty_available() { ( : </dev/tty ) 2>/dev/null; }

ask() {
  # $1: soru; yanıt REPLY_TEXT değişkenine yazılır
  REPLY_TEXT=""
  printf '%s' "$1" >/dev/tty
  read -r REPLY_TEXT </dev/tty || REPLY_TEXT=""
}

state_get() {
  [[ -f "$STATE_FILE" ]] || return 0
  sed -n "s/^${1}=//p" "$STATE_FILE" | tail -n 1
}

state_clear() {
  local key=$1 tmp
  [[ -f "$STATE_FILE" ]] || return 0
  tmp=$(mktemp "${STATE_FILE}.XXXXXX")
  grep -v "^${key}=" "$STATE_FILE" >"$tmp" || true
  chown root:root "$tmp"
  chmod 0600 "$tmp"
  mv -f -- "$tmp" "$STATE_FILE"
}

env_get() {
  local value=""
  [[ -f "$ENV_FILE" ]] || return 0
  value=$(sed -n "s/^${1}=//p" "$ENV_FILE" | tail -n 1)
  value=${value%$'\r'}
  printf '%s' "$value"
}

parse_args() {
  while (( $# )); do
    case "$1" in
      --level)             [[ $# -ge 2 ]] || die "--level bir değer gerektirir."; LEVEL=$2; shift 2 ;;
      --level=*)           LEVEL=${1#*=}; shift ;;
      --yes|-y)            OPT_YES=1; shift ;;
      --confirm-data-loss) OPT_CONFIRM_DATA=1; shift ;;
      --remove-volumes)    OPT_REMOVE_VOLUMES=1; shift ;;
      --help|-h)           usage; exit 0 ;;
      *)                   usage >&2; die "Bilinmeyen seçenek: $1" ;;
    esac
  done
  if [[ -n "$LEVEL" && ! "$LEVEL" =~ ^[123]$ ]]; then
    die "Düzey geçersiz: \"$LEVEL\". 1, 2 veya 3 olmalıdır."
  fi
  if (( OPT_YES )) && [[ -z "$LEVEL" ]]; then
    die "--yes kullanılırken --level de verilmelidir."
  fi
  if (( OPT_YES )) && [[ "$LEVEL" != 1 ]] && (( ! OPT_CONFIRM_DATA )); then
    die "Düzey $LEVEL verileri ve yedekleri siler. Etkileşimsiz kullanımda --confirm-data-loss da verilmelidir."
  fi
  if (( OPT_REMOVE_VOLUMES )) && [[ "$LEVEL" != 3 ]]; then
    die "--remove-volumes yalnızca --level 3 ile kullanılabilir."
  fi
}

choose_level() {
  [[ -z "$LEVEL" ]] || return 0
  tty_available || die "Etkileşimli terminal yok. Düzeyi --level ile verin (ayrıntı: --help)."
  cat >/dev/tty <<'EOF'

MyServer kaldırma

  1. Yalnızca paneli kaldır
     Program, yardımcı, servis ve sudoers kuralı kaldırılır.
     /var/lib/myserver ve /etc/myserver korunur.

  2. Panel + config
     Ek olarak /etc/myserver ve /var/lib/myserver silinir
     (panel veritabanı, uygulama verileri ve YEDEKLER dahil).

  3. Panel + tüm uygulamalar
     Ek olarak MyServer'ın kurduğu kapsayıcılar ve ağlar kaldırılır.
     Docker birimleri için ayrıca onay istenir.

  0. Vazgeç

EOF
  ask "Seçiminiz [0-3]: "
  case "$REPLY_TEXT" in
    1|2|3) LEVEL=$REPLY_TEXT ;;
    *) printf 'Vazgeçildi; hiçbir şey değiştirilmedi.\n'; exit 0 ;;
  esac
}

docker_usable() { have docker && docker info >/dev/null 2>&1; }

collect_volumes() {
  VOLUMES=()
  local v
  while IFS= read -r v; do
    [[ -n "$v" ]] && VOLUMES+=("$v")
  done < <(docker volume ls -q --filter "label=$LABEL" 2>/dev/null || true)
}

confirm_all() {
  if (( OPT_YES )); then
    if (( OPT_REMOVE_VOLUMES )); then REMOVE_VOLUMES=1; fi
    return 0
  fi
  tty_available || die "Etkileşimli terminal yok. --yes ve gerekli onay seçeneklerini kullanın (ayrıntı: --help)."

  if [[ "$LEVEL" == 1 ]]; then
    printf '\nPanel kaldırılacak. /var/lib/myserver ve /etc/myserver korunacak.\n' >/dev/tty
    ask "Devam edilsin mi? [e/H]: "
    [[ "$REPLY_TEXT" =~ ^[eEyY]$ ]] || { printf 'Vazgeçildi; hiçbir şey değiştirilmedi.\n'; exit 0; }
    return 0
  fi

  cat >/dev/tty <<EOF

${C_RED}DİKKAT - GERİ ALINAMAZ${C_OFF}
Aşağıdaki dizinler içindeki HER ŞEYLE birlikte silinecek:
  $CONF_DIR        (panel yapılandırması)
  $DATA_DIR    (panel veritabanı, uygulama verileri ve TÜM YEDEKLER:
                        $DATA_DIR/backups)
Yedeklerinizi saklamak istiyorsanız şimdi başka bir yere kopyalayın.
EOF
  if [[ "$LEVEL" == 3 ]]; then
    printf 'Ayrıca "%s" etiketli kapsayıcılar ve ağlar kaldırılacak.\n' "$LABEL" >/dev/tty
  fi
  ask "Onaylamak için $WORD_DATA yazın: "
  [[ "$REPLY_TEXT" == "$WORD_DATA" ]] || { printf 'Onay verilmedi; hiçbir şey değiştirilmedi.\n'; exit 0; }

  if [[ "$LEVEL" == 3 ]] && docker_usable; then
    collect_volumes
    if (( ${#VOLUMES[@]} )); then
      {
        printf '\n%sDocker birimleri (uygulama verileri)%s\n' "$C_BOLD" "$C_OFF"
        printf 'MyServer tarafından oluşturulmuş şu birimler bulundu:\n'
        printf '  - %s\n' "${VOLUMES[@]}"
        printf 'Bu birimler uygulamalarınızın verilerini içerir. Silinirse geri getirilemez.\n'
        printf 'Onaylamazsanız birimler KORUNUR, kaldırmanın geri kalanı sürer.\n'
      } >/dev/tty
      ask "Birimleri de silmek için $WORD_VOLUMES yazın (korumak için Enter): "
      if [[ "$REPLY_TEXT" == "$WORD_VOLUMES" ]]; then REMOVE_VOLUMES=1; fi
    fi
  fi
}

remove_apps() {
  step "Uygulamaların kaldırılması"
  if ! docker_usable; then
    warn "Docker'a ulaşılamıyor; kapsayıcılar ve ağlar kaldırılamadı. Docker'ı başlatıp betiği yeniden çalıştırabilirsiniz."
    return 0
  fi
  local ids=() id nets=() kept=() v
  while IFS= read -r id; do
    [[ -n "$id" ]] && ids+=("$id")
  done < <(docker ps -aq --filter "label=$LABEL")

  if (( ${#ids[@]} )); then
    # Etiketsiz (MyServer'ın oluşturmadığı) birimler yalnızca bilgi için listelenir.
    while IFS= read -r v; do
      [[ -n "$v" ]] && kept+=("$v")
    done < <(docker inspect --format '{{range .Mounts}}{{if eq .Type "volume"}}{{println .Name}}{{end}}{{end}}' "${ids[@]}" 2>/dev/null | sort -u || true)
    docker stop --time 20 "${ids[@]}" >/dev/null 2>&1 || true
    # "-v" BİLEREK kullanılmaz: birimler bu adımda silinmez.
    docker rm -f "${ids[@]}" >/dev/null
    ok "${#ids[@]} kapsayıcı kaldırıldı"
  else
    info "MyServer tarafından yönetilen kapsayıcı yok"
  fi

  while IFS= read -r id; do
    [[ -n "$id" ]] && nets+=("$id")
  done < <(docker network ls -q --filter "label=$LABEL")
  if (( ${#nets[@]} )); then
    for id in "${nets[@]}"; do
      docker network rm "$id" >/dev/null 2>&1 \
        || warn "Ağ kaldırılamadı (başka bir kapsayıcı kullanıyor olabilir): $id"
    done
    ok "MyServer ağları kaldırıldı"
  fi

  collect_volumes
  if (( ${#VOLUMES[@]} )); then
    if (( REMOVE_VOLUMES )); then
      for v in "${VOLUMES[@]}"; do
        docker volume rm "$v" >/dev/null 2>&1 \
          || warn "Birim silinemedi (kullanımda olabilir): $v"
      done
      ok "MyServer Docker birimleri silindi"
    else
      info "Şu Docker birimleri KORUNDU (silmek için: docker volume rm <ad>):"
      printf '    - %s\n' "${VOLUMES[@]}"
    fi
  fi
  if (( ${#kept[@]} )); then
    local unlabelled=() k
    for k in "${kept[@]}"; do
      if ! docker volume inspect --format '{{index .Labels "io.myserver.managed"}}' "$k" 2>/dev/null | grep -qx true; then
        unlabelled+=("$k")
      fi
    done
    if (( ${#unlabelled[@]} )); then
      info "Kaldırılan kapsayıcıların kullandığı, MyServer etiketi taşımayan birimlere dokunulmadı:"
      printf '    - %s\n' "${unlabelled[@]}"
    fi
  fi
}

remove_service() {
  step "Servisin kaldırılması"
  if systemctl list-unit-files "${SERVICE}.service" >/dev/null 2>&1 || [[ -f "$UNIT_PATH" ]]; then
    systemctl stop "$SERVICE" >/dev/null 2>&1 || true
    systemctl disable "$SERVICE" >/dev/null 2>&1 || true
  fi
  # Servis KillMode=process kullanır; panelin alt süreçleri durdurmadan sonra
  # kalmış olabilir. Yalnızca "myserver" kullanıcısına ait olanlar sonlandırılır.
  local procs="/sys/fs/cgroup/system.slice/${SERVICE}.service/cgroup.procs"
  if getent passwd "$SVC_USER" >/dev/null && pgrep -u "$SVC_USER" >/dev/null 2>&1; then
    pkill -TERM -u "$SVC_USER" >/dev/null 2>&1 || true
    sleep 2
    pkill -KILL -u "$SVC_USER" >/dev/null 2>&1 || true
  fi
  if [[ -r "$procs" ]] && grep -q . "$procs" 2>/dev/null; then
    warn "Panelin başlattığı yetkili bir işlem (örneğin paket güncellemesi) hâlâ çalışıyor; ona dokunulmadı."
    warn "Süreç numaraları: $(tr '\n' ' ' <"$procs")"
  fi
  rm -f -- "$UNIT_PATH"
  systemctl daemon-reload
  systemctl reset-failed "$SERVICE" >/dev/null 2>&1 || true
  ok "Servis durduruldu ve kaldırıldı; systemd yeniden yüklendi"
}

remove_firewall_rule() {
  step "Güvenlik duvarı kuralı"
  local port from
  port=$(state_get UFW_RULE_PORT)
  from=$(state_get UFW_RULE_FROM)
  if [[ -z "$port" || -z "$from" ]]; then
    info "Kurulum betiğinin eklediği bir kural kayıtlı değil; güvenlik duvarına dokunulmadı"
    return 0
  fi
  if [[ ! "$port" =~ ^[0-9]{1,5}$ || ! "$from" =~ ^(any|[0-9]{1,3}(\.[0-9]{1,3}){3}/[0-9]{1,2})$ ]]; then
    warn "Kayıtlı kural bilgisi geçersiz; güvenlik duvarına dokunulmadı."
    return 0
  fi
  if ! have ufw; then
    info "UFW kurulu değil; kaldırılacak kural yok"
  elif [[ "$from" == any ]]; then
    ufw delete allow "${port}/tcp" >/dev/null 2>&1 || warn "UFW kuralı silinemedi: ${port}/tcp"
    ok "UFW kuralı kaldırıldı: ${port}/tcp"
  else
    ufw delete allow from "$from" to any port "$port" proto tcp >/dev/null 2>&1 \
      || warn "UFW kuralı silinemedi: $from -> ${port}/tcp"
    ok "UFW kuralı kaldırıldı: $from -> ${port}/tcp"
  fi
  state_clear UFW_RULE_PORT
  state_clear UFW_RULE_FROM
  info "Diğer güvenlik duvarı kurallarına ve UFW'nin durumuna dokunulmadı."
}

remove_files() {
  step "Program dosyaları"
  rm -f -- "$SUDOERS_PATH"
  ok "sudoers kuralı kaldırıldı"
  rm -f -- "$BIN_PATH" "$HELPER_PATH"
  rm -rf --one-file-system -- "$SHARE_DIR"
  rm -f -- /run/myserver-update.lock
  # Yalnızca günlük ve sonuç dosyaları içerir; panel olmadan anlamı yoktur.
  rm -rf --one-file-system -- "$UPDATES_DIR"
  ok "Program dosyaları kaldırıldı"
}

remove_data() {
  step "Yapılandırma ve veriler"
  local custom
  custom=$(env_get MYSERVER_DATA_DIR)
  rm -rf --one-file-system -- "$CONF_DIR"
  ok "Silindi: $CONF_DIR"
  rm -rf --one-file-system -- "$DATA_DIR" 2>/dev/null || true
  if [[ -e "$DATA_DIR" ]]; then
    warn "$DATA_DIR tümüyle silinemedi (ayrı bir disk bağlanmış olabilir). İçeriği elle denetleyin."
  else
    ok "Silindi: $DATA_DIR (yedekler dahil)"
  fi
  if [[ -n "$custom" && "$custom" != "$DATA_DIR" ]]; then
    warn "Yapılandırmada özel bir veri dizini tanımlıydı: $custom"
    warn "Bu dizine DOKUNULMADI. Gerekirse kendiniz silin."
  fi
}

remove_user() {
  step "Sistem kullanıcısı"
  if getent passwd "$SVC_USER" >/dev/null; then
    # Ev dizini silinmez (-r kullanılmaz): veri dizini düzeye göre ayrıca ele alınır.
    if userdel "$SVC_USER" >/dev/null 2>&1; then
      ok "\"$SVC_USER\" kullanıcısı kaldırıldı"
    else
      warn "\"$SVC_USER\" kullanıcısı kaldırılamadı (hâlâ çalışan bir süreci olabilir): sudo userdel $SVC_USER"
    fi
  fi
  if getent group "$SVC_GROUP" >/dev/null; then
    groupdel "$SVC_GROUP" >/dev/null 2>&1 || true
  fi
}

main() {
  parse_args "$@"
  [[ ${EUID:-$(id -u)} -eq 0 ]] || die "Bu betik root yetkisiyle çalıştırılmalıdır. Örnek: sudo $0"

  # Betik panel servisinin içinden (web terminali) çalıştırılırsa servis
  # durdurulduğunda kendisi de sonlanır ve kaldırma yarıda kalır.
  if [[ -r /proc/self/cgroup ]] && grep -q "/${SERVICE}\.service" /proc/self/cgroup; then
    die "Bu betik panelin web terminalinden çalıştırılamaz. SSH veya konsol oturumu kullanın."
  fi

  choose_level
  confirm_all

  if [[ "$LEVEL" == 3 ]]; then remove_apps; fi
  remove_service
  remove_firewall_rule
  remove_files
  if [[ "$LEVEL" != 1 ]]; then remove_data; fi
  remove_user

  printf '\n%sMyServer kaldırıldı.%s\n' "$C_BOLD" "$C_OFF"
  if [[ "$LEVEL" == 1 ]]; then
    printf 'Korunan dizinler: %s, %s\n' "$DATA_DIR" "$CONF_DIR"
    printf 'Yeniden kurduğunuzda verileriniz ve ayarlarınız kullanılmaya devam eder.\n'
  fi
  printf 'Docker ve kurulum sırasında eklenen sistem paketleri kaldırılmadı.\n'
}

main "$@"
