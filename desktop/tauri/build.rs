// The app's own commands get ACL permissions (allow-studio-shell and so on),
// so a capability can grant them to the Studio's origin and nothing else.
fn main() {
    tauri_build::try_build(tauri_build::Attributes::new().app_manifest(
        tauri_build::AppManifest::new().commands(&["studio_shell", "retry", "open_logs", "smoke"]),
    ))
    .expect("tauri build step failed");
}
