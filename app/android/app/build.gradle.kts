plugins {
    id("com.android.application")
    // The Flutter Gradle Plugin must be applied after the Android and Kotlin Gradle plugins.
    id("dev.flutter.flutter-gradle-plugin")
}

android {
    namespace = "dev.godbtw.brod"
    compileSdk = flutter.compileSdkVersion
    ndkVersion = flutter.ndkVersion

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    defaultConfig {
        // TODO: Specify your own unique Application ID (https://developer.android.com/studio/build/application-id.html).
        applicationId = "dev.godbtw.brod"
        // You can update the following values to match your application needs.
        // For more information, see: https://flutter.dev/to/review-gradle-config.
        minSdk = flutter.minSdkVersion
        targetSdk = flutter.targetSdkVersion
        // Uses the version code from pubspec.yaml. When using split APKs, 1000 * ABI_VERSION
        // is added automatically by Flutter. (https://developer.android.com/studio/build/configure-apk-splits#configure-APK-versions)
        // You can force using the value of versionCode by specifying the `-P force-version-code-ignoring-abi=true`
        // flag during build.
        versionCode = flutter.versionCode
        versionName = flutter.versionName
    }

    // Подпись release: постоянный ключ из окружения (CI, секреты репозитория). Без него release-сборка падает (APK с отладочным ключом
    // не встанет поверх настоящего); осознанно собрать с отладочным ключом — BROD_ALLOW_DEBUG_SIGNING=1.
    // Постоянный ключ нужен, чтобы обновления ставились поверх без удаления приложения и его данных.
    val brodKeystore = System.getenv("BROD_KEYSTORE")
    // BROD_UNSIGNED=1 — release без подписи вообще (app-release-unsigned.apk); его подписывает отдельное задание CI
    val brodUnsigned = System.getenv("BROD_UNSIGNED") == "1" && brodKeystore == null
    signingConfigs {
        if (brodKeystore != null) {
            create("brod") {
                storeFile = file(brodKeystore)
                storePassword = System.getenv("BROD_KS_PASS")
                keyAlias = System.getenv("BROD_KEY_ALIAS") ?: "brod"
                keyPassword = System.getenv("BROD_KS_PASS")
            }
        }
    }

    gradle.taskGraph.whenReady {
        if (allTasks.any { it.name == "packageRelease" } && brodKeystore == null && !brodUnsigned && System.getenv("BROD_ALLOW_DEBUG_SIGNING") != "1") {
            throw GradleException("release без ключа подписи: задайте BROD_KEYSTORE и BROD_KS_PASS (BROD_UNSIGNED=1 для неподписанного APK, BROD_ALLOW_DEBUG_SIGNING=1 для отладочного ключа)")
        }
    }

    buildTypes {
        release {
            signingConfig = when {
                brodKeystore != null -> signingConfigs.getByName("brod")
                brodUnsigned -> null
                else -> signingConfigs.getByName("debug")
            }
        }
    }
}

kotlin {
    compilerOptions {
        jvmTarget = org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17
    }
}

dependencies {
    // ядро (androidcore + libbox), собирается в CI: gomobile bind, см. .github/workflows/build.yml
    implementation(fileTree(mapOf("dir" to "libs", "include" to listOf("*.aar"))))
    // FileProvider для «Поделиться журналом»
    implementation("androidx.core:core:1.13.1")
}

flutter {
    source = "../.."
}
