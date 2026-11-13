#!/bin/sh
# clipshare 便携包安装脚本（tgz 版本）。
#
# 与 deb 的区别：不经过包管理器，可以装到任意前缀（默认 /usr/local），
# 也不需要 root —— 把 PREFIX 指到家目录即可。卸载用 uninstall.sh。
set -eu

PREFIX="${PREFIX:-/usr/local}"
BINDIR="$PREFIX/bin"
DOCDIR="$PREFIX/share/doc/clipshare"
# systemd 用户单元：/usr/local 前缀下放到 $PREFIX/lib/systemd/user
UNITDIR="$PREFIX/lib/systemd/user"

say() { printf '%s\n' "$*"; }

if [ "$(id -u)" -ne 0 ] && [ "$PREFIX" = "/usr/local" ]; then
    say "提示：安装到 $PREFIX 需要写权限，请用 sudo 运行，或指定 PREFIX："
    say "      PREFIX=\$HOME/.local ./install.sh"
    exit 1
fi

say "==> 安装到 $PREFIX"
mkdir -p "$BINDIR" "$DOCDIR" "$UNITDIR"
install -m 0755 clipshare "$BINDIR/clipshare"
for f in README.md README.html guide.html guide.pdf; do
    [ -f "$f" ] && install -m 0644 "$f" "$DOCDIR/$f"
done
install -m 0644 clipshare.service "$UNITDIR/clipshare.service"

# 装到标准前缀时顺手启用：否则用户看到「装好了」却什么都没跑，体验很差。
# 临时前缀（测试/自定义目录）下不动 systemd —— 那里的 unit 本来就不在搜索路径里。
case "$PREFIX" in
    /usr|/usr/local|"$HOME/.local")
        if command -v systemctl >/dev/null 2>&1; then
            systemctl --user daemon-reload 2>/dev/null || true
            if systemctl --user enable --now clipshare.service 2>/dev/null; then
                say ""
                say "已设置为开机自启并启动。"
            fi
        fi
        ;;
esac

say ""
say "已安装：$BINDIR/clipshare"
say "文档：  $DOCDIR/guide.html"
say ""
say "启用（当前用户，无需 root）："
say "    systemctl --user daemon-reload"
say "    systemctl --user enable --now clipshare"
say ""
say "若 $UNITDIR 不在 systemd 的搜索路径里，改成用户级安装更省事："
say "    PREFIX=\$HOME/.local ./install.sh"
say ""
say "别忘了剪贴板依赖：sudo apt install xclip   # 或 wl-clipboard（Wayland）"
