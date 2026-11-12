#!/usr/bin/env bash
# 打两个安装包 + 两版文档到 dist/。
#
#   dist/clipshare_<ver>_amd64.deb              Ubuntu 安装包（含 systemd user unit）
#   dist/clipshare-<ver>-windows-amd64.zip      Windows 安装包（含 install.ps1）
#   dist/README.html                            给人看的（单文件，样式内嵌，可离线）
#   dist/README.md                              给 AI 看的（结构化原文）
#   dist/SHA256SUMS                             校验和
#
# 用法：packaging/build.sh [版本号]   （默认取 Makefile 里的 VERSION）
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

VERSION="${1:-$(sed -n 's/^VERSION ?= //p' Makefile | head -1)}"
VERSION="${VERSION:-0.1.0}"
ARCH="$(go env GOARCH)"
DIST="$ROOT/dist"
DOCS="README.md README.html"

echo "==> 版本 $VERSION / 架构 $ARCH"
rm -rf "$DIST" && mkdir -p "$DIST"

# ---------- 文档 ----------
echo "==> 生成文档"
pandoc README.md -f gfm -t html5 --standalone --toc --toc-depth=2 \
    --metadata title="clipshare — 局域网剪贴板共享" --metadata lang=zh-CN \
    --self-contained -c packaging/html/style.css \
    -o "$DIST/README.html"
cp README.md "$DIST/README.md"

# ---------- 使用手册（含配图） ----------
echo "==> 生成使用手册"
mkdir -p "$DIST/docs/images"
python3 packaging/docs/build-guide.py "$ROOT" "$DIST/docs"
if command -v google-chrome >/dev/null 2>&1; then
    google-chrome --headless=new --disable-gpu --no-pdf-header-footer \
        --print-to-pdf="$DIST/docs/guide.pdf" "file://$DIST/docs/guide.html" >/dev/null 2>&1 \
        && echo "    guide.pdf 已生成" || echo "    ! PDF 生成失败（HTML 仍可用）"
else
    echo "    ! 未安装 google-chrome，跳过 PDF"
fi

# ---------- 二进制 ----------
echo "==> 构建二进制"
CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -trimpath \
    -ldflags "-s -w -X main.version=$VERSION" -o "$DIST/clipshare" ./cmd/clipshare
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath \
    -ldflags "-s -w -X main.version=$VERSION -H windowsgui" -o "$DIST/clipshare.exe" ./cmd/clipshare

# ---------- Ubuntu .deb ----------
echo "==> 打包 deb"
DEBROOT="$DIST/.deb"
mkdir -p "$DEBROOT/DEBIAN" "$DEBROOT/usr/bin" \
         "$DEBROOT/usr/share/doc/clipshare" "$DEBROOT/usr/lib/systemd/user"
install -m 0755 "$DIST/clipshare" "$DEBROOT/usr/bin/clipshare"
install -m 0644 "$DIST/README.md" "$DEBROOT/usr/share/doc/clipshare/README.md"
install -m 0644 "$DIST/README.html" "$DEBROOT/usr/share/doc/clipshare/README.html"
install -m 0644 "$DIST/docs/guide.html" "$DEBROOT/usr/share/doc/clipshare/guide.html"
[ -f "$DIST/docs/guide.pdf" ] && install -m 0644 "$DIST/docs/guide.pdf" "$DEBROOT/usr/share/doc/clipshare/guide.pdf"
install -m 0644 packaging/deb/clipshare.service "$DEBROOT/usr/lib/systemd/user/clipshare.service"
install -m 0755 packaging/deb/postinst "$DEBROOT/DEBIAN/postinst"
install -m 0755 packaging/deb/prerm "$DEBROOT/DEBIAN/prerm"

SIZE_KB=$(( $(du -sk "$DEBROOT" | cut -f1) ))
cat > "$DEBROOT/DEBIAN/control" <<CTRL
Package: clipshare
Version: $VERSION
Architecture: $ARCH
Maintainer: clipshare <noreply@localhost>
Installed-Size: $SIZE_KB
Section: utils
Priority: optional
Recommends: xclip | xsel | wl-clipboard, zenity
Conflicts: clipshare-static
Description: 局域网剪贴板共享（对称 P2P，托盘常驻）
 同房间的机器之间自动同步文本剪贴板，保留带来源标记的历史；
 提供 Ctrl+Alt+V 选择窗口与本地历史页。单文件二进制，CGO_ENABLED=0，
 运行时不依赖任何 Go 代码或第三方库。
 .
 房间密钥只以 Bearer 头出网，mDNS/health 只暴露其 sha256 前 12 位。
 明文传输，安全边界见文档「安全边界」一节。
CTRL

DEB_NAME="clipshare_${VERSION}_${ARCH}.deb"
dpkg-deb --build --root-owner-group "$DEBROOT" "$DIST/$DEB_NAME" >/dev/null
rm -rf "$DEBROOT"

# ---------- Windows setup.exe（NSIS） ----------
echo "==> 打包 Windows 安装程序（NSIS）"
NSISTAGE="$DIST/.nsis"
mkdir -p "$NSISTAGE"
cp "$DIST/clipshare.exe" "$NSISTAGE/"
cp "$DIST/README.md" "$DIST/README.html" "$NSISTAGE/"
cp "$DIST/docs/guide.html" "$NSISTAGE/"
[ -f "$DIST/docs/guide.pdf" ] && cp "$DIST/docs/guide.pdf" "$NSISTAGE/"
cp packaging/windows/setup.nsi "$NSISTAGE/"
if command -v makensis >/dev/null 2>&1; then
    ( cd "$NSISTAGE" && makensis -V2 -DVERSION="$VERSION" -DPORT=47100 setup.nsi >/dev/null )
    mv "$NSISTAGE/clipshare-$VERSION-windows-setup.exe" "$DIST/"
else
    echo "    ! 未安装 makensis，跳过 setup.exe（Ubuntu: sudo apt install nsis）"
fi
rm -rf "$NSISTAGE"

# ---------- Windows zip（免安装版） ----------
echo "==> 打包 Windows"
WINTREE="$DIST/clipshare-${VERSION}-windows-amd64"
mkdir -p "$WINTREE"
cp "$DIST/clipshare.exe" "$WINTREE/"
cp packaging/windows/install.ps1 packaging/windows/uninstall.ps1 packaging/windows/安装.bat "$WINTREE/"
cp "$DIST/README.md" "$DIST/README.html" "$WINTREE/"
cp "$DIST/docs/guide.html" "$WINTREE/"
[ -f "$DIST/docs/guide.pdf" ] && cp "$DIST/docs/guide.pdf" "$WINTREE/"
WIN_ZIP="$DIST/clipshare-${VERSION}-windows-amd64.zip"
( cd "$DIST" && zip -qr "$WIN_ZIP" "clipshare-${VERSION}-windows-amd64" )
rm -rf "$WINTREE"
rm -f "$DIST/clipshare" "$DIST/clipshare.exe"

# ---------- 校验和 ----------
echo "==> 生成校验和"
( cd "$DIST" && sha256sum "$DEB_NAME" "$(basename "$WIN_ZIP")" clipshare-"$VERSION"-windows-setup.exe README.md README.html > SHA256SUMS 2>/dev/null \
    || sha256sum "$DEB_NAME" "$(basename "$WIN_ZIP")" README.md README.html > SHA256SUMS )

echo
echo "完成，产物在 $DIST："
ls -lh "$DIST" | sed 's/^/  /'
