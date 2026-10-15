plugins {
    id("com.android.application")
}

val bundledServerSource = rootProject.layout.projectDirectory.file("../scrcpy/server/build/outputs/apk/release/server-release-unsigned.apk")
val bundledServerAssetDir = layout.buildDirectory.dir("generated/allrelay-assets")

val prepareBundledServer by tasks.registering(Copy::class) {
    from(bundledServerSource)
    into(bundledServerAssetDir)
    rename { "allrelay.jar" }
    doFirst {
        if (!bundledServerSource.asFile.exists()) {
            throw GradleException("Missing Android server artifact. Run ./scripts/build-apk.sh.")
        }
    }
}

android {
    namespace = "com.allrelay.app"
    compileSdk = 34

    sourceSets.getByName("main").assets.srcDir(bundledServerAssetDir.get().asFile)

    defaultConfig {
        applicationId = "com.allrelay.app"
        minSdk = 31
        targetSdk = 34
        versionCode = 4
        versionName = "0.2.19"
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            proguardFiles(
                getDefaultProguardFile("proguard-android-optimize.txt"),
                "proguard-rules.pro"
            )
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

}

tasks.named("preBuild") {
    dependsOn(prepareBundledServer)
}

dependencies {
    implementation("androidx.core:core-ktx:1.12.0")
    implementation("androidx.appcompat:appcompat:1.6.1")
    implementation("com.google.android.material:material:1.11.0")

    testImplementation("junit:junit:4.13.2")
}
