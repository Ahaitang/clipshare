#!/usr/bin/env bash
# 手工打 APK（不依赖 Gradle）：
#   aapt2 编译/链接资源 → javac → d8 生成 dex → 塞进 APK → zipalign → apksigner 签名
#
# 为什么不用 Gradle：这个 App 只有 1 个 Activity、0 个第三方依赖，
# 拉一整套 Gradle + AGP（首次构建要下几百 MB）不划算，手工链路更透明也更快。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

SDK="${ANDROID_HOME:-/opt/android-sdk}"
BT="$(ls -d "$SDK"/build-tools/* | sort -V | tail -1)"
PLATFORM="$(ls -d "$SDK"/platforms/android-* | sort -V | tail -1)/android.jar"
if [ ! -f "$PLATFORM" ]; then
    echo "找不到 android.jar（ANDROID_HOME=$SDK）" >&2
    exit 1
fi

VERSION="${1:-0.1.0}"
OUT="$ROOT/build/android"
DIST="$ROOT/dist"
rm -rf "$OUT" && mkdir -p "$OUT/compiled" "$OUT/gen" "$OUT/classes" "$OUT/dex" "$DIST"

echo "==> 资源编译与链接（aapt2）"
"$BT/aapt2" compile --dir packaging/android/res -o "$OUT/compiled/res.zip"
"$BT/aapt2" link \
    -o "$OUT/base.apk" \
    -I "$PLATFORM" \
    --manifest packaging/android/AndroidManifest.xml \
    -R "$OUT/compiled/res.zip" \
    --java "$OUT/gen" \
    --min-sdk-version 24 --target-sdk-version 34 \
    --version-code 1 --version-name "$VERSION" \
    --auto-add-overlay

echo "==> Java 编译（javac）"
javac -source 8 -target 8 -nowarn -Xlint:-options \
    -classpath "$PLATFORM" \
    -d "$OUT/classes" \
    $(find packaging/android/src "$OUT/gen" -name '*.java')

echo "==> 生成 dex（d8）"
"$BT/d8" --lib "$PLATFORM" --min-api 24 --output "$OUT/dex" \
    $(find "$OUT/classes" -name '*.class')

echo "==> 组装 APK（写入 classes.dex）"
python3 - "$OUT/base.apk" "$OUT/dex/classes.dex" <<'PY'
import sys, zipfile
apk, dex = sys.argv[1], sys.argv[2]
with zipfile.ZipFile(apk, "a", zipfile.ZIP_DEFLATED) as z:
    z.write(dex, "classes.dex")
print("   classes.dex 已写入")
PY

echo "==> zipalign"
"$BT/zipalign" -f -p 4 "$OUT/base.apk" "$OUT/aligned.apk"

# 签名密钥：自签的调试密钥，密码公开（见 README）。它的作用是「同一把钥匙能覆盖安装」，
# 不承担任何保密职责 —— 请不要拿它当发布密钥。
KS="packaging/android/clipshare-debug.jks"
if [ ! -f "$KS" ]; then
    echo "==> 生成自签调试密钥"
    keytool -genkeypair -v -keystore "$KS" -alias clipshare -keyalg RSA -keysize 2048 \
        -validity 10000 -storepass clipshare -keypass clipshare \
        -dname "CN=clipshare sender, OU=dev, O=clipshare, C=CN" >/dev/null 2>&1
fi

echo "==> apksigner 签名"
APK="$DIST/clipshare-sender-${VERSION}.apk"
# --v4-signing-enabled false：否则会在 APK 旁边留一个 .idsig 附属文件，
# 它只对增量安装（adb install --incremental）有用，放进发布包只是噪音。
"$BT/apksigner" sign --ks "$KS" --ks-pass pass:clipshare --key-pass pass:clipshare \
    --v4-signing-enabled false \
    --out "$APK" "$OUT/aligned.apk"
"$BT/apksigner" verify --print-certs "$APK" | head -3

echo "==> 校验"
"$BT/aapt2" dump badging "$APK" | grep -E "^package|launchable-activity|uses-permission|sdkVersion|targetSdkVersion" | sed 's/^/   /'
ls -lh "$APK" | awk '{print "   产物:", $9, $5}'
