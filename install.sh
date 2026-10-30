#!/usr/bin/env bash
# Установка и обновление коробки мониторинга «Дозор» (репозиторий Sentra).
#
#   curl -fsSL https://raw.githubusercontent.com/AbuBakrVu/Sentra/main/install.sh | sudo bash
#
# Повторный запуск той же команды обновляет коробку до свежей версии из GitHub.
# Необязательные переменные:
#   SENTRA_IP=192.168.1.10   адрес коробки (по умолчанию определяется сам)
#   SENTRA_DIR=/opt/sentra   куда ставить
#   SENTRA_BRANCH=main       ветка
set -euo pipefail

REPO="${SENTRA_REPO:-https://github.com/AbuBakrVu/Sentra.git}"
BRANCH="${SENTRA_BRANCH:-main}"
DIR="${SENTRA_DIR:-/opt/sentra}"

say()  { printf '\n\033[1m%s\033[0m\n' "$*"; }
fail() { printf '\n\033[31mОшибка: %s\033[0m\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || fail "запустите от root: curl ... | sudo bash"
[ "$(uname -s)" = "Linux" ] || fail "коробка ставится только на Linux"

install_pkg() {
  if command -v apt-get >/dev/null 2>&1; then
    apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "$@" >/dev/null
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y -q "$@"
  elif command -v yum >/dev/null 2>&1; then
    yum install -y -q "$@"
  else
    fail "не удалось установить $*: поставьте вручную и запустите скрипт снова"
  fi
}

say "1/5 Проверяю зависимости"
command -v curl >/dev/null 2>&1 || install_pkg curl
command -v git  >/dev/null 2>&1 || install_pkg git
if ! command -v docker >/dev/null 2>&1; then
  echo "Docker не найден, устанавливаю..."
  curl -fsSL https://get.docker.com | sh
fi
systemctl enable --now docker >/dev/null 2>&1 || true
docker compose version >/dev/null 2>&1 || fail "нет плагина docker compose (пакет docker-compose-plugin)"

say "2/5 Загружаю код из GitHub"
if [ -d "$DIR/.git" ]; then
  git -C "$DIR" fetch -q --depth 1 origin "$BRANCH"
  git -C "$DIR" reset -q --hard "origin/$BRANCH"
  echo "Обновлено до $(git -C "$DIR" log -1 --format='%h %s')"
else
  git clone -q --depth 1 -b "$BRANCH" "$REPO" "$DIR"
  echo "Скачано в $DIR"
fi
cd "$DIR"

say "3/5 Настраиваю"
if [ ! -f .env ]; then
  IP="${SENTRA_IP:-$(ip route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src"){print $(i+1); exit}}')}"
  [ -n "$IP" ] || fail "не удалось определить IP. Запустите с SENTRA_IP=<адрес коробки>"
  cat > .env <<EOF
DOZOR_PUBLIC_URL=https://$IP
DOZOR_CERT_HOSTS=$IP
DOZOR_ADMIN_PASSWORD=
TZ=Europe/Moscow
EOF
  chmod 600 .env
  echo "Адрес коробки: https://$IP (поменять можно в $DIR/.env)"
else
  echo "Настройки уже есть, оставляю $DIR/.env без изменений"
fi
IP_URL="$(grep '^DOZOR_PUBLIC_URL=' .env | cut -d= -f2-)"

say "4/5 Собираю и запускаю (первый раз 3–5 минут)"
docker compose up -d --build

say "5/5 Жду запуска панели"
for _ in $(seq 1 60); do
  curl -sk --max-time 2 https://127.0.0.1/api/health >/dev/null 2>&1 && break
  sleep 2
done
curl -sk --max-time 2 https://127.0.0.1/api/health >/dev/null 2>&1 \
  || fail "панель не ответила. Журнал: cd $DIR && docker compose logs dozor"

PASS="$(docker compose exec -T dozor cat /var/lib/dozor/initial-admin-password.txt 2>/dev/null || true)"

say "Готово"
echo "Панель:  $IP_URL"
echo "Логин:   admin"
if [ -n "$PASS" ]; then
  echo "Пароль:  $PASS   (смените после входа в разделе «Настройки»)"
else
  echo "Пароль:  тот, что вы задали или сменили раньше"
fi
echo
echo "Браузер предупредит о самоподписанном сертификате: это нормально, подтвердите исключение."
echo "Обновление: запустите эту же команду ещё раз."
