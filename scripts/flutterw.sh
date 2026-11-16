#!/usr/bin/env bash
# Обёртка над flutter: SDK в ~/dev/toolchains/flutter; официальное хранилище Google отдаёт 403 из нашей сети —
# берём зеркало flutter-io.cn (предусмотрено документацией Flutter для сетей с ограниченным доступом).
export PATH="$HOME/dev/toolchains/flutter/bin:$PATH"
export CI=${CI:-true} FLUTTER_SUPPRESS_ANALYTICS=true
export FLUTTER_STORAGE_BASE_URL=${FLUTTER_STORAGE_BASE_URL:-https://storage.flutter-io.cn}
export PUB_HOSTED_URL=${PUB_HOSTED_URL:-https://pub.flutter-io.cn}
exec flutter "$@"
