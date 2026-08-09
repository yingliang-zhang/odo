# App Identity and Filesystem Layout

- Tauri identifier stays `com.yingliangzhang.odo`: the bundle ID is the macOS app identity, and changing it resets permissions and Tauri stores for zero value (epoch-1)
- Local directory `~/Projects/odo` is NOT renamed: 7 live worktrees plus the running daemon reference the path, making it a cosmetic change with real risk (epoch-2)
- Op2 (release build + install to /Applications) was deferred by the user, but `/Applications/Odo.app` was built at 18:01 and running since 18:04 outside logged sessions [INFERENCE: user completed it] (epoch-4)
