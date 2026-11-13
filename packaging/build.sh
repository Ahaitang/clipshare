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
        && { python3 packaging/docs/normalize-pdf.py "$DIST/docs/guide.pdf"; echo "    guide.pdf 已生成"; } \
        || echo "    ! PDF 生成失败（HTML 仍可用）"
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

# 固定 mtime：否则 deb 里每个文件的 tar 时间戳随构建时间变化，包就不可复现
find "$DEBROOT" -exec touch -d @0 {} + 2>/dev/null || touch -t 197001010000 "$DEBROOT"

DEB_NAME="clipshare_${VERSION}_${ARCH}.deb"
SOURCE_DATE_EPOCH=0 dpkg-deb --build --root-owner-group "$DEBROOT" "$DIST/$DEB_NAME" >/dev/null
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

# tar 的可复现参数：排序 + 固定属主/时间，同样的输入得到同样的包
TAR_FLAGS="--sort=name --owner=0 --group=0 --numeric-owner --mtime=@0"

# ---------- Windows zip + tgz（免安装版） ----------
echo "==> 打包 Windows"
WINTREE="$DIST/clipshare-${VERSION}-windows-amd64"
mkdir -p "$WINTREE"
cp "$DIST/clipshare.exe" "$WINTREE/"
cp packaging/windows/install.ps1 packaging/windows/uninstall.ps1 packaging/windows/安装.bat "$WINTREE/"
cp "$DIST/README.md" "$DIST/README.html" "$WINTREE/"
cp "$DIST/docs/guide.html" "$WINTREE/"
[ -f "$DIST/docs/guide.pdf" ] && cp "$DIST/docs/guide.pdf" "$WINTREE/"
WIN_ZIP="$DIST/clipshare-${VERSION}-windows-amd64.zip"
# zip 会把每个文件的 mtime 与额外属性写进去；固定时间 + -X 才能得到可复现的包
find "$WINTREE" -exec touch -d @0 {} +
( cd "$DIST" && zip -qrX "$WIN_ZIP" "clipshare-${VERSION}-windows-amd64" )
# 同一个目录再打一份 tgz：有人用 zip，有人习惯 tar.gz
( cd "$DIST" && tar $TAR_FLAGS -czf "clipshare-${VERSION}-windows-amd64.tar.gz" "clipshare-${VERSION}-windows-amd64" )
rm -rf "$WINTREE"

# ---------- tgz 便携包（两个平台） ----------
# 为什么还要 tgz：deb 需要包管理器和 root，zip 在 Linux 上要额外一步解压；
# tar.gz 两边都能直接解压，配合包里的 install.sh / install.ps1 就能落地。
echo "==> 打包 tgz（Linux 便携版）"

LINUX_TREE="$DIST/clipshare-${VERSION}-linux-amd64"
mkdir -p "$LINUX_TREE"
cp "$DIST/clipshare" "$LINUX_TREE/"
cp "$DIST/README.md" "$DIST/README.html" "$LINUX_TREE/"
cp "$DIST/docs/guide.html" "$LINUX_TREE/"
[ -f "$DIST/docs/guide.pdf" ] && cp "$DIST/docs/guide.pdf" "$LINUX_TREE/"
cp packaging/deb/clipshare.service "$LINUX_TREE/"
cp packaging/linux/install.sh packaging/linux/uninstall.sh "$LINUX_TREE/"
chmod 0755 "$LINUX_TREE/install.sh" "$LINUX_TREE/uninstall.sh"
LINUX_TGZ="$DIST/clipshare-${VERSION}-linux-amd64.tar.gz"
( cd "$DIST" && tar $TAR_FLAGS -czf "$LINUX_TGZ" "clipshare-${VERSION}-linux-amd64" )
rm -rf "$LINUX_TREE"


rm -f "$DIST/clipshare" "$DIST/clipshare.exe"
# ---------- Android 发送端 APK ----------
# 只发送、不接收：它是一个纯上行客户端（不监听端口），所以桌面端根本发现不到它。
echo "==> 打包 Android 发送端（如已具备 Android SDK）"
if [ -x packaging/android/build-apk.sh ] && { [ -n "${ANDROID_HOME:-}" ] || [ -d /opt/android-sdk ]; }; then
    if packaging/android/build-apk.sh "$VERSION" >/dev/null 2>&1; then
        echo "    clipshare-sender-$VERSION.apk 已生成"
    else
        echo "    ! APK 构建失败（不影响其它产物；单独跑 packaging/android/build-apk.sh 可看日志）"
    fi
else
    echo "    ! 未找到 Android SDK（ANDROID_HOME 或 /opt/android-sdk），跳过 APK"
fi

# ---------- 校验和 ----------
echo "==> 生成校验和"
( cd "$DIST" && : > SHA256SUMS
  for f in "$DEB_NAME" "$(basename "$WIN_ZIP")" \
           "clipshare-${VERSION}-windows-setup.exe" \
           "clipshare-sender-${VERSION}.apk" \
           "clipshare-${VERSION}-linux-amd64.tar.gz" \
           "clipshare-${VERSION}-windows-amd64.tar.gz" \
           README.md README.html docs/guide.html docs/guide.pdf; do
      [ -f "$f" ] && sha256sum "$f" >> SHA256SUMS
  done )

# ---------- 整包（把 dist 里所有东西打成一条 tgz） ----------
# 用途：离线交付/存档，一个文件拿走全部安装包与文档。
echo "==> 打包整包 tgz"
BUNDLE="clipshare-${VERSION}-all.tar.gz"
( cd "$DIST" && ls | grep -v "^$BUNDLE$" > .bundle-list && tar $TAR_FLAGS -czf "$BUNDLE" -T .bundle-list && rm -f .bundle-list )
( cd "$DIST" && sha256sum "$BUNDLE" >> SHA256SUMS )

echo
echo "完成，产物在 $DIST："
ls -lh "$DIST" | sed 's/^/  /'
