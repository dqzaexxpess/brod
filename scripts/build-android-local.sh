#!/usr/bin/env bash
# Локальная сборка Android без GitHub Actions: ядро (AAR, gomobile) и подписанный APK.
#   scripts/build-android-local.sh setup   — один раз: Android SDK/NDK, gomobile, зеркало Maven для Gradle
#   scripts/build-android-local.sh build   — ядро и APK → app/build/app/outputs/flutter-apk/app-release.apk
#   scripts/build-android-local.sh aar     — только ядро;  apk — только приложение (ядро уже собрано)
# Зачем: dl.google.com и Google Maven недоступны из некоторых сетей (404), а CI доступен не всегда. Допустимые источники:
#   Android SDK/NDK — зеркало mirrors.cloud.tencent.com/AndroidSDK (SHA-256 из официального dl.google.com, сверены 2026-11-15, зеркало их не удостоверяет);
#   Google Maven — maven.aliyun.com/repository/google (только для этой сборки: отдельный GRADLE_USER_HOME); Flutter — flutter-io.cn (scripts/flutterw.sh).
# Это сторонние зеркала: код плагина Gradle исполняется при сборке, а APK подписывается постоянным ключом. Нужен JDK 17 с javac.
# Подпись: ~/.config/brod/android-release.jks и android-release.pass (тот же ключ, что в CI; без него обновления не встанут поверх).
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT=$PWD
TC=$HOME/dev/toolchains
export ANDROID_HOME=$TC/android-sdk ANDROID_SDK_ROOT=$TC/android-sdk
export GRADLE_USER_HOME=$TC/gradle-home-mirror
export JAVA_HOME=${JAVA_HOME_17:-/usr/lib/jvm/java-17-openjdk-amd64}
export PATH=$JAVA_HOME/bin:$HOME/go/bin:$PATH
NDK_CORE=27.3.13750724   # ядро (как в CI)
NDK_FLUTTER=28.2.13676358 # ndkVersion Flutter 3.47.9 для приложения
B=https://mirrors.cloud.tencent.com/AndroidSDK

fetch() { # архив sha256 каталог
  local f=$1 sum=$2 dest=$3 dl=$TC/android-dl
  [ -d "$dest" ] && return
  mkdir -p "$dl" "$(dirname "$dest")"
  for _ in 1 2 3 4 5 6; do curl -fsS -C - -o "$dl/$f" "$B/$f" && break || sleep 3; done
  echo "$sum  $dl/$f" | sha256sum -c -
  mkdir -p "$dl/x_$f" && unzip -q -o "$dl/$f" -d "$dl/x_$f" && mv "$dl/x_$f"/* "$dest" && rmdir "$dl/x_$f" && rm -f "$dl/$f"
}

setup() {
  [ -x "$JAVA_HOME/bin/javac" ] || { echo "нужен JDK 17 с javac (JAVA_HOME_17)"; exit 1; }
  fetch platform-36_r02.zip 37607369a28c5b640b3a7998868d45898ebcb777565a0e85f9acf36f29631d2e "$ANDROID_HOME/platforms/android-36"
  fetch build-tools_r36_linux.zip 5d9ac77fb6ff43d9da518a337b4fcf8f9097113df531d99ccefe80ef7ce8250b "$ANDROID_HOME/build-tools/36.0.0"
  fetch android-ndk-r27d-linux.zip 601246087a682d1944e1e16dd85bc6e49560fe8b6d61255be2829178c8ed15d9 "$ANDROID_HOME/ndk/$NDK_CORE"
  fetch android-ndk-r28c-linux.zip dfb20d396df28ca02a8c708314b814a4d961dc9074f9a161932746f815aa552f "$ANDROID_HOME/ndk/$NDK_FLUTTER"
  mkdir -p "$ANDROID_HOME/licenses"; cp /usr/lib/android-sdk/licenses/* "$ANDROID_HOME/licenses/" 2>/dev/null || true
  (cd /tmp && go install github.com/sagernet/gomobile/cmd/gomobile@v0.1.13 && go install github.com/sagernet/gomobile/cmd/gobind@v0.1.13)
  mkdir -p "$GRADLE_USER_HOME/init.d"
  cat > "$GRADLE_USER_HOME/init.d/google-mirror.gradle" <<'EOG'
// Только для локальной сборки Brod: адрес Google Maven подменяется зеркалом Aliyun, остальные репозитории не трогаются.
def mirror = 'https://maven.aliyun.com/repository/google'
def rewrite = { repo ->
    if (repo instanceof MavenArtifactRepository && repo.url.toString().startsWith('https://dl.google.com/dl/android/maven2')) {
        repo.url = mirror
    }
}
gradle.beforeSettings { s ->
    s.pluginManagement.repositories.all(rewrite)
    s.dependencyResolutionManagement.repositories.all(rewrite)
}
gradle.allprojects { p ->
    p.buildscript.repositories.all(rewrite)
    p.repositories.all(rewrite)
}
EOG
  echo "setup готов"
}

aar() {
  scripts/fetch-engine.sh
  mkdir -p app/android/app/libs
  ANDROID_NDK_HOME=$ANDROID_HOME/ndk/$NDK_CORE gomobile bind -v -o app/android/app/libs/brodcore.aar -target android/arm64 -androidapi 24 -javapkg=io.nekohasekai -libname=box \
    -trimpath -buildvcs=false -ldflags="-checklinkname=0 -s -w -X runtime.godebugDefault=madvdontneed=1" -tags "$(cat scripts/tags.txt)" \
    brod/androidcore github.com/sagernet/sing-box/experimental/libbox
}

apk() {
  export BROD_KEYSTORE=$HOME/.config/brod/android-release.jks
  [ -f "$BROD_KEYSTORE" ] || { echo "нет ключа подписи $BROD_KEYSTORE"; exit 1; }
  BROD_KS_PASS="$(cat "$HOME/.config/brod/android-release.pass")"; export BROD_KS_PASS
  (cd app && ../scripts/flutterw.sh build apk --release --target-platform android-arm64)
  scripts/verify-flutter-engine.sh app/build/app/outputs/flutter-apk/app-release.apk # движок сверен с официальным хранилищем
  echo "APK: $ROOT/app/build/app/outputs/flutter-apk/app-release.apk"
}

case "${1:-build}" in
  setup) setup ;;
  aar) aar ;;
  apk) apk ;;
  build) aar; apk ;;
  *) echo "использование: $0 setup|build|aar|apk"; exit 2 ;;
esac
