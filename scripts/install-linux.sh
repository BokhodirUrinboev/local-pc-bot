#!/usr/bin/env bash
# Remofy bot — Linux (Ubuntu) o'rnatuvchi. Botni systemd service sifatida
# o'rnatadi: boot'da auto-start + crash'da qayta ishga tushadi.
#
# Ishlatish (loyiha ildizidan):
#   sudo ./scripts/install-linux.sh
#
# Sozlamalar (env orqali override qilsa bo'ladi):
#   INSTALL_DIR   — o'rnatish papkasi (default: /opt/remofy-bot)
#   SERVICE_USER  — service qaysi user ostida ishlaydi (default: sudo chaqirgan user)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

INSTALL_DIR="${INSTALL_DIR:-/opt/remofy-bot}"
SERVICE_USER="${SERVICE_USER:-${SUDO_USER:-$(id -un)}}"
UNIT_PATH="/etc/systemd/system/remofy-bot.service"

if [[ "${EUID}" -ne 0 ]]; then
  echo "Bu skript root huquqini talab qiladi: sudo $0" >&2
  exit 1
fi

if ! command -v go >/dev/null 2>&1; then
  echo "go topilmadi. Avval Go o'rnating: https://go.dev/dl/" >&2
  exit 1
fi

echo ">> Build (linux/amd64)…"
cd "$PROJECT_ROOT"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags='-s -w' -o ".dist/remofy-bot" ./cmd/bot

echo ">> O'rnatish papkasi: $INSTALL_DIR (user: $SERVICE_USER)"
mkdir -p "$INSTALL_DIR"
install -m 0755 ".dist/remofy-bot" "$INSTALL_DIR/remofy-bot"

# .env: agar install papkada hali yo'q bo'lsa — loyihadagisini yoki .example'ni qo'yamiz.
if [[ ! -f "$INSTALL_DIR/.env" ]]; then
  if [[ -f "$PROJECT_ROOT/.env" ]]; then
    install -m 0600 "$PROJECT_ROOT/.env" "$INSTALL_DIR/.env"
  else
    install -m 0600 "$PROJECT_ROOT/.env.example" "$INSTALL_DIR/.env"
    echo "!! $INSTALL_DIR/.env namuna sifatida yaratildi — TELEGRAM_BOT_TOKEN va ALLOWED_TELEGRAM_IDS to'ldiring."
  fi
fi
chown -R "$SERVICE_USER" "$INSTALL_DIR"

echo ">> systemd unit: $UNIT_PATH"
sed -e "s|@USER@|$SERVICE_USER|g" \
    -e "s|@INSTALL_DIR@|$INSTALL_DIR|g" \
    "$SCRIPT_DIR/remofy-bot.service" > "$UNIT_PATH"

systemctl daemon-reload
systemctl enable --now remofy-bot.service

echo
echo "Tayyor. Boshqaruv:"
echo "  systemctl status remofy-bot"
echo "  journalctl -u remofy-bot -f      # jonli loglar"
echo "  sudo nano $INSTALL_DIR/.env && sudo systemctl restart remofy-bot"
echo
echo "ESLATMA: Claude rejimi uchun shu user ($SERVICE_USER) ostida 'claude login'"
echo "bajarilgan bo'lishi shart (token ~/.claude/ ichida)."
