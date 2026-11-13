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

# 只有「systemd 实际加载的那份 unit 就在我们要删的目录里」时才停服务。
#
# 这条判断是必须的：脚本支持 PREFIX，如果在临时前缀下测试卸载，
# 无条件 systemctl disable --now 会把**真实安装**的服务停掉并取消自启
# （实测踩到：一次临时前缀的卸载测试让正在跑的服务停了 18 小时、且丢掉自启）。
fragment="$(systemctl --user show -p FragmentPath --value clipshare.service 2>/dev/null || true)"
if [ -n "$fragment" ] && [ "$fragment" = "$UNITDIR/clipshare.service" ]; then
    printf '==> 停止并取消自启（%s）\n' "$fragment"
    systemctl --user disable --now clipshare.service 2>/dev/null || true
else
    printf '==> 跳过服务操作：当前 systemd 加载的不是 %s（FragmentPath=%s）\n' \
        "$UNITDIR/clipshare.service" "${fragment:-无}"
fi

rm -f "$BINDIR/clipshare" "$UNITDIR/clipshare.service"
rm -rf "$DOCDIR"
systemctl --user daemon-reload 2>/dev/null || true

printf '已卸载。你的配置与历史仍保留在 ~/.config/clipshare（需要彻底清除请手动删除）。\n'
