# Fallback for platforms whose WebKitGTK cannot run the Studio: a launcher that
# starts the Studio's server and opens it in Firefox. No WebKitGTK and no
# compiled shell in it. The Studio itself and Node are not in it yet, as in the
# other packages.
#
# rpmbuild -bb --define "_topdir DIR" --define "ver 0.0.1" activate-studio-browser.spec
# with the launcher, the desktop file and the icon in DIR/SOURCES.
%global debug_package %{nil}

Name:           activate-studio-browser
Version:        %{?ver}%{!?ver:0.0.1}
Release:        1%{?dist}
Summary:        ACTIVATE Studio launcher that opens the Studio in Firefox
License:        Apache-2.0
URL:            https://github.com/parallelworks/activate-studio
BuildArch:      noarch
Requires:       firefox
Requires:       bash >= 5.1
Requires:       curl
Requires:       util-linux

%description
Starts the Studio's server on a free loopback port and opens it in a Firefox
window with a profile of its own. The server stops when that window closes.

%install
install -Dm755 %{_sourcedir}/activate-studio-browser %{buildroot}%{_bindir}/activate-studio-browser
install -Dm644 %{_sourcedir}/activate-studio-browser.desktop %{buildroot}%{_datadir}/applications/activate-studio-browser.desktop
install -Dm644 %{_sourcedir}/activate-studio-browser.png %{buildroot}%{_datadir}/icons/hicolor/512x512/apps/activate-studio-browser.png

%files
%{_bindir}/activate-studio-browser
%{_datadir}/applications/activate-studio-browser.desktop
%{_datadir}/icons/hicolor/512x512/apps/activate-studio-browser.png
