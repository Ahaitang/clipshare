#!/bin/sh
# 在中心点 A 上一键部署 clipshare 中继。
#
# 做的事：建用户 → 装二进制 → 建配置目录（首次启动自动生成自签证书）→ 装 systemd 单元。
# 只监听 127.0.0.1，对外请用内网穿透/反向代理映射一个公网端口（推荐纯 TCP 透传）。
set -eu

BIN_SRC="${1:-./clipshare}"
PREFIX="${PREFIX:-/usr/local}"
CONF_DIR="${CONF_DIR:-/etc/clipshare}"
HUB_LISTEN="${HUB_LISTEN:-127.0.0.1:3011}"
ROOM_KEY="${ROOM_KEY:-}"
SERVICE=/etc/systemd/system/clipshare-hub.service

if [ "$(id -u)" -ne 0 ]; then
    echo "请用 sudo 运行（要建用户、装 systemd 单元）" >&2
    exit 1
fi
[ -f "$BIN_SRC" ] || { echo "找不到二进制: $BIN_SRC" >&2; exit 1; }

if ! id clipshare >/dev/null 2>&1; then
    useradd --system --no-create-home --shell /usr/sbin/nologin clipshare
fi

install -m 0755 "$BIN_SRC" "$PREFIX/bin/clipshare"
install -d -o clipshare -g clipshare -m 0700 "$CONF_DIR"

if [ ! -f "$CONF_DIR/config.json" ]; then
    if [ -z "$ROOM_KEY" ]; then
        ROOM_KEY=$(tr -dc 'abcdefghjkmnpqrstuvwxyz23456789' </dev/urandom | head -c 16)
        echo "未指定 ROOM_KEY，已生成随机房间码: $ROOM_KEY"
    fi
    DEV_ID=$(tr -dc '0-9a-f' </dev/urandom | head -c 32)
    cat > "$CONF_DIR/config.json" <<JSON
{
  "nodeName": "hub-a",
  "deviceId": "$DEV_ID",
  "roomKey": "$ROOM_KEY",
  "lanEnabled": false,
  "hubEnabled": true,
  "hubListen": "$HUB_LISTEN",
  "hubKeep": 200,
  "hubAllowPlaintext": false,
  "port": 47100,
  "seeds": [],
  "historyLimit": 200,
  "itemMaxBytes": 262144,
  "autoApply": true,
  "mdns": false,
  "persist": false,
  "pollIntervalMs": 400,
  "noTray": true
}
JSON
    chown clipshare:clipshare "$CONF_DIR/config.json"
    chmod 600 "$CONF_DIR/config.json"
fi

install -m 0644 "$(dirname "$0")/clipshare-hub.service" "$SERVICE"
systemctl daemon-reload
systemctl enable --now clipshare-hub
sleep 2

echo
echo "=== 部署完成 ==="
systemctl is-active clipshare-hub >/dev/null && echo "服务: 运行中" || echo "服务: 未运行（看 journalctl -u clipshare-hub）"
echo
sudo -u clipshare "$PREFIX/bin/clipshare" hub-info --config-dir "$CONF_DIR" || \
    "$PREFIX/bin/clipshare" hub-info --config-dir "$CONF_DIR" || true
echo
echo "下一步："
echo "  1. 把上面的【证书指纹】填到 B/C 的 relayPin；"
echo "  2. 用内网穿透把 $HUB_LISTEN 映射到一个公网端口（纯 TCP，不要做 TLS 终止）；"
echo "  3. B/C 的 relayURL 填 https://<公网地址>:<端口>。"
