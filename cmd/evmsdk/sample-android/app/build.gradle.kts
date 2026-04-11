// Minimal Compose app that consumes the gomobile-bound evmsdk.aar.
// Intentionally tiny — relies on Android Studio's defaults for everything
// not strictly required.

plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "io.n42.verifier"
    compileSdk = 34

    defaultConfig {
        applicationId = "io.n42.verifier"
        minSdk = 24
        targetSdk = 34
        versionCode = 1
        versionName = "0.1.0"
    }

    buildFeatures {
        compose = true
    }

    composeOptions {
        kotlinCompilerExtensionVersion = "1.5.4"
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlinOptions {
        jvmTarget = "17"
    }

    // The gomobile-bound evmsdk.aar lives in app/libs/.
    // Drop it there after running `make android` from the repo root.
    sourceSets {
        getByName("main") {
            // The aar dep is added via `implementation(files(...))` below.
        }
    }
}

dependencies {
    // gomobile-bound bridge to cmd/evmsdk
    implementation(files("libs/evmsdk.aar"))

    // Compose BOM keeps versions consistent
    val composeBom = platform("androidx.compose:compose-bom:2024.02.00")
    implementation(composeBom)
    implementation("androidx.compose.material3:material3")
    implementation("androidx.compose.ui:ui-tooling-preview")
    debugImplementation("androidx.compose.ui:ui-tooling")
    implementation("androidx.activity:activity-compose:1.8.2")

    // Coroutines (used by EvmsdkWrapper for stats polling)
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.7.3")

    // org.json comes with the Android SDK; no extra dep needed.
}
