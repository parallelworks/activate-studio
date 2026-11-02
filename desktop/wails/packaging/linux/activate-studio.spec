# RPM of the shell for Rocky Linux 9 and RHEL 9 (WebKitGTK 4.0 build).
# The Studio itself (server/dist, web/dist, node_modules) and a Node runtime
# are not in it yet, as in the .deb.
#
# rpmbuild -bb --define "_topdir DIR" --define "ver 0.0.1" activate-studio.spec
# with the binary, the desktop file and the icon in DIR/SOURCES.
%global debug_package %{nil}
%global __os_install_post %{nil}
%global _build_id_links none

Name:           activate-studio
Version:        %{?ver}%{!?ver:0.0.1}
Release:        1%{?dist}
Summary:        ACTIVATE Studio desktop shell (Wails v2 spike)
License:        Apache-2.0
URL:            https://github.com/parallelworks/activate-studio
ExclusiveArch:  x86_64
Requires:       webkit2gtk3
Requires:       gtk3

%description
A native window around the ACTIVATE Studio server. Built against the
WebKitGTK 4.0 API (webkit2gtk3, libsoup 2).

%install
install -Dm755 %{_sourcedir}/activate-studio %{buildroot}/opt/activate-studio/activate-studio
install -Dm644 %{_sourcedir}/activate-studio.desktop %{buildroot}%{_datadir}/applications/activate-studio.desktop
install -Dm644 %{_sourcedir}/activate-studio.png %{buildroot}%{_datadir}/icons/hicolor/512x512/apps/activate-studio.png

%files
/opt/activate-studio/activate-studio
%{_datadir}/applications/activate-studio.desktop
%{_datadir}/icons/hicolor/512x512/apps/activate-studio.png
