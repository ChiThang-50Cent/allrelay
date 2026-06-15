plugins {
    id("com.android.application")
}

val bundledServerSource = rootProject.layout.projectDirectory.file("../bin/scrcpy-server-allrelay")
val bundledServerAssetDir = layout.buildDirectory.dir("generated/allrelay-assets")

val prepareBundledServer by tasks.registering(Copy::class) {
    from(bundledServerSource)
    into(bundledServerAssetDir)
    rename { "allrelay.jar" }
    doFirst {
        if (!bundledServerSource.asFile.exists()) {
            throw GradleException("Missing ../bin/scrcpy-server-allrelay. Build scrcpy server first.")
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
        versionCode = 2
        versionName = "0.2.0"
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
}
