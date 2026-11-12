#!/usr/bin/env bash
# Установка и обновление коробки мониторинга Sentra.
#
#   curl -fsSL https://raw.githubusercontent.com/AbuBakrVu/Sentra/main/install.sh | sudo bash
#
# Повторный запуск той же команды обновляет коробку до свежей версии из GitHub.
# После установки доступна команда sentra:
#   sudo sentra info      адрес панели, логин и пароль первого входа
#   sudo sentra update    обновить до свежей версии (с той же ветки, с которой ставили)
#   sudo sentra status    состояние контейнеров
#   sudo sentra logs      журнал сервера
#
# Необязательные переменные:
#   SENTRA_IP=192.168.1.10   адрес коробки (по умолчанию определяется сам)
#   SENTRA_DIR=/opt/sentra   куда ставить
#   SENTRA_BRANCH=main       ветка
#
# Весь скрипт обёрнут в main(): при запуске через «curl | bash» bash читает
# скрипт из канала, и любая команда, читающая stdin, иначе «съела» бы его остаток.
set -euo pipefail

REPO="${SENTRA_REPO:-https://github.com/AbuBakrVu/Sentra.git}"
BRANCH="${SENTRA_BRANCH:-main}"
DIR="${SENTRA_DIR:-/opt/sentra}"
RAW="https://raw.githubusercontent.com/AbuBakrVu/Sentra/${BRANCH}/install.sh"

say()  { printf '\n\033[1m%s\033[0m\n' "$*"; }
fail() { printf '\n\033[31mОшибка: %s\033[0m\n' "$*" >&2; exit 1; }

install_pkg() {
  if command -v apt-get >/dev/null 2>&1; then
    apt-get update -qq </dev/null && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "$@" </dev/null >/dev/null
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y -q "$@" </dev/null
  elif command -v yum >/dev/null 2>&1; then
    yum install -y -q "$@" </dev/null
  else
    fail "не удалось установить $*: поставьте вручную и запустите скрипт снова"
  fi
}

# Адрес, логин и пароль. Пароль первого входа сервер хранит в файле,
# пока администратор не сменит его в панели.
show_info() {
  cd "$DIR" 2>/dev/null || fail "Sentra не установлена в $DIR"
  local url pass
  url="$(grep '^SENTRA_PUBLIC_URL=' .env 2>/dev/null | cut -d= -f2-)"
  pass="$(docker compose exec -T sentra cat /var/lib/sentra/initial-admin-password.txt </dev/null 2>/dev/null | tr -d '\r\n' || true)"
  printf '\n\033[1;32m%s\033[0m\n' "Sentra работает"
  printf '  Панель:  \033[1m%s\033[0m\n' "${url:-https://<IP этого сервера>}"
  printf '  Логин:   \033[1m%s\033[0m\n' "admin"
  if [ -n "$pass" ]; then
    printf '  Пароль:  \033[1m%s\033[0m\n' "$pass"
    echo
    echo "  Смените пароль после входа: раздел «Настройки»."
  else
    echo "  Пароль:  уже сменён в панели (первоначальный больше не хранится)"
  fi
  echo
  echo "  Браузер предупредит о самоподписанном сертификате: это нормально, подтвердите исключение."
  echo "  Показать это ещё раз: sudo sentra info"
}

install_cli() {
  cat > /usr/local/bin/sentra <<EOF
#!/usr/bin/env bash
# Управление коробкой Sentra
set -e
DIR="$DIR"
case "\${1:-}" in
  info)    exec bash "\$DIR/install.sh" --info ;;
  update)  curl -fsSL "$RAW" | SENTRA_BRANCH="$BRANCH" SENTRA_DIR="$DIR" bash ;;
  status)  cd "\$DIR" && exec docker compose ps ;;
  logs)    cd "\$DIR" && exec docker compose logs --tail 100 -f sentra ;;
  restart) cd "\$DIR" && exec docker compose restart ;;
  *) echo "Использование: sentra info | update | status | logs | restart"; exit 2 ;;
esac
EOF
  chmod 755 /usr/local/bin/sentra
}

main() {
  [ "$(id -u)" -eq 0 ] || fail "запустите от root: curl ... | sudo bash"
  [ "$(uname -s)" = "Linux" ] || fail "коробка ставится только на Linux"

  if [ "${1:-}" = "--info" ]; then
    show_info
    return
  fi

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
    git -C "$DIR" fetch -q --depth 1 origin "+refs/heads/$BRANCH:refs/remotes/origin/$BRANCH" </dev/null \
      || fail "не удалось скачать ветку $BRANCH из GitHub"
    git -C "$DIR" reset -q --hard "origin/$BRANCH"
    echo "Обновлено до $(git -C "$DIR" log -1 --format='%h %s')"
  else
    git clone -q --depth 1 -b "$BRANCH" "$REPO" "$DIR" </dev/null
    echo "Скачано в $DIR"
  fi
  cd "$DIR"

  say "3/5 Настраиваю"
  if [ ! -f .env ]; then
    local ip
    ip="${SENTRA_IP:-$(ip route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src"){print $(i+1); exit}}')}"
    [ -n "$ip" ] || fail "не удалось определить IP. Запустите с SENTRA_IP=<адрес коробки>"
    cat > .env <<EOF
SENTRA_PUBLIC_URL=https://$ip
SENTRA_CERT_HOSTS=$ip
SENTRA_ADMIN_PASSWORD=
TZ=Europe/Moscow
EOF
    chmod 600 .env
    echo "Адрес коробки: https://$ip (поменять можно в $DIR/.env)"
  else
    echo "Настройки уже есть, оставляю $DIR/.env без изменений"
  fi
  install_cli

  say "4/5 Собираю и запускаю (первый раз 3–5 минут)"
  docker compose up -d --build </dev/null

  say "5/5 Жду запуска панели"
  local ok=""
  for _ in $(seq 1 60); do
    if curl -sk --max-time 2 https://127.0.0.1/api/health >/dev/null 2>&1; then ok=1; break; fi
    sleep 2
  done
  [ -n "$ok" ] || fail "панель не ответила за 2 минуты. Журнал: sudo sentra logs"

  show_info
}

main "$@"
