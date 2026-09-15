# Manual test keystore

`test-release.jks` is intentionally a public, test-only Android signing
keystore. It signs `../apks/test-release.apk`.

- Alias: `release`
- Store password: `storepass`
- Key password: `storepass`
- APK: `../apks/test-release.apk` (`dev.zapstore.testrelease`)

Regenerate the APK with `./generate-apk`.

Never use this keystore or password for a real application.
