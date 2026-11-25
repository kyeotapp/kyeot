# Phone notifications (APNs)

**English** · [한국어](push.ko.md)

The kyeot server can send notifications to an iPhone while the app is closed. It sends them when an agent finishes a task (success or failure) and when it asks for an approval or a choice. Routine run results work the same way. While the app is open, the screen shows things right away, so nothing is sent. iOS drops the WebSocket when the app goes to the background, so the server treats "no connected apps at all" as "the app is closed".

Notifications go through APNs (Apple Push Notification service), Apple's push server. To prove it may send to APNs, the server needs a key from the Apple Developer Program. Without key settings, the server keeps notifications off and does everything else as usual. It also keeps accepting `POST /api/devices`, which the app uses to register the device.

## After you join

On the Apple Developer site, go to Certificates, Identifiers & Profiles, then Keys. Create a new key and check Apple Push Notifications service (APNs). The downloaded `AuthKey_<KeyID>.p8` can be downloaded only once, so do not lose it. The key ID is on the key detail screen, and the team ID is on the Membership screen. If the bundle ID is not `app.kyeot`, note that value too.

Put the key file in the config folder on the server Mac and make it readable only by you.

```sh
mkdir -p ~/.config/kyeot
mv ~/Downloads/AuthKey_*.p8 ~/.config/kyeot/apns.p8
chmod 600 ~/.config/kyeot/apns.p8
```

Write the key ID and team ID in `~/.config/kyeot/apns.env`. Set this file's permission to 600 too.

```sh
cat > ~/.config/kyeot/apns.env <<'EOF'
KYEOT_APNS_KEY_ID=ABCDE12345
KYEOT_APNS_TEAM_ID=FGHIJ67890
# Only when the bundle ID is different
# KYEOT_APNS_TOPIC=app.kyeot
EOF
chmod 600 ~/.config/kyeot/apns.env
```

`kyeot` does not read these two files by itself. When starting it, pass `KYEOT_APNS_KEY` (path to the key file), `KYEOT_APNS_KEY_ID`, and `KYEOT_APNS_TEAM_ID` as environment variables. All three must be present to turn it on; if only some are present, it logs what is missing on a `push off:` line. `kyeot service install` writes these variables from the current shell into the service file, so registering once as below is enough.

```sh
set -a; . ~/.config/kyeot/apns.env; set +a
KYEOT_APNS_KEY=~/.config/kyeot/apns.p8 kyeot service install
```

Restart the server. If the log shows a `push on: APNs (topic …)` line, it worked. If the key file cannot be read or has the wrong format, a line starting with `push off:` appears, and the server keeps running without notifications. The key contents and the signed token are never logged, and only the first 8 characters of a device token are logged.

```sh
launchctl kickstart -k gui/$(id -u)/app.kyeot.server
```

## APNs server per device

Development builds installed directly from Xcode must receive from the sandbox server, and TestFlight or App Store builds from the production server. The app reports which one with `env` when it registers the device, and the server picks based on the value stored per device. If APNs answers 410 or `Unregistered`, the server deletes that device. When the app opens again, it registers again. `BadDeviceToken` is also returned when the wrong environment was chosen, so the server tries once more with the other environment and deletes the device only if that also returns the same answer. If it goes through there, the notification was delivered, and it reports `ErrWrongEnv`.

Temporary errors are retried. For 429, 5xx, and network errors, it sends two more times after 0.5 seconds and 2 seconds. For a 403 saying the signing token (JWT) expired or is invalid (`ExpiredProviderToken`, `InvalidProviderToken`), it signs a new token and sends once more. The signing token is replaced every 50 minutes by wall-clock time. Time spent while the Mac sleeps counts as elapsed.

## App side

The app must be built with `KYEOT_PUSH=1` to ask for notification permission and register the device.

The separate switch exists because of free Apple IDs (Personal Team). To receive notifications, the app must include the `aps-environment` entitlement, but a free team cannot sign with it, so installation breaks entirely. And `expo-notifications` adds this entitlement just by being installed. So `app/app.config.js` removes the entitlement when the switch is off and adds the plugin when it is on. An app built with it off never calls notification code at all (`app/src/lib/push.ts`).

After joining, change `ios.appleTeamId` in `app/app.json` to the paid team ID and install like this. Builds installed from Xcode use the sandbox server.

```sh
KYEOT_PUSH=1 sh scripts/ios-install.sh
```

For builds shipped through TestFlight or the App Store, also set `KYEOT_PUSH_ENV=production`. Each time the app connects, it checks notification permission and hands the device token to `POST /api/devices`. Tapping a notification opens that conversation. This also works when the app was closed and opened from the notification.

Check that the switch works with the prebuild output. When it is off, the command below prints 0.

```sh
cd app && npx expo prebuild -p ios && grep -c aps-environment ios/kyeot/kyeot.entitlements
```
