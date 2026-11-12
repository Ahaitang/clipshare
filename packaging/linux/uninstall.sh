#!/bin/sh
# clipshare 便携包卸载脚本。只会删除安装进去的文件，**不动**你的配置与历史。
set -eu

PREFIX="${PREFIX:-/usr/local}"
BINDIR="$PREFIX/bin"
DOCDIR="$PREFIX/share/doc/clipshare"
UNITDIR="$PREFIX/lib/systemd/user"

if [ "$(id -u)" -ne 0 ] && [ "$PREFIX" = "/usr/local" ]; then
    printf '请用 sudo 运行，或指定 PREFIX（例如 PREFIX=%s/.local）\n' "$HOME" >&2
    exit 1
fi

printf '==> 停止服务（若在运行）\n'
systemctl --user disable --now clipshare.service 2>/dev/null || true

rm -f "$BINDIR/clipshare" "$UNITDIR/clipshare.service"
rm -rf "$DOCDIR"
systemctl --user daemon-reload 2>/dev/null || true

printf '已卸载。你的配置与历史仍保留在 ~/.config/clipshare（需要彻底清除请手动删除）。\n'
