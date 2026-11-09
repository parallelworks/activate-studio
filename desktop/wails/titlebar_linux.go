//go:build linux

package main

/*
#cgo pkg-config: gtk+-3.0
#include <stdlib.h>
#include <gtk/gtk.h>

static GtkCssProvider *bar_css;

static gboolean bar_apply(gpointer css) {
  GdkScreen *screen = gdk_screen_get_default();
  if (screen) {
    if (!bar_css) {
      bar_css = gtk_css_provider_new();
      gtk_style_context_add_provider_for_screen(screen, GTK_STYLE_PROVIDER(bar_css),
        GTK_STYLE_PROVIDER_PRIORITY_USER);
    }
    gtk_css_provider_load_from_data(bar_css, css, -1, NULL);
  }
  g_free(css);
  return G_SOURCE_REMOVE;
}

// GTK's own thread applies it, ahead of drawing.
static void bar_set(const char *css) {
  g_idle_add_full(G_PRIORITY_HIGH, bar_apply, g_strdup(css), NULL);
}

// Runs when any widget is shown, until the window has its bar. GTK shows a
// window's children before the window, and a title bar has to be in place
// before the window is realized, so the first child shown is the moment.
static gboolean bar_attach(GSignalInvocationHint *hint, guint n, const GValue *params, gpointer title) {
  GtkWidget *top = gtk_widget_get_toplevel(GTK_WIDGET(g_value_get_object(&params[0])));
  if (!GTK_IS_WINDOW(top) || !gtk_widget_is_toplevel(top)) return TRUE;
  if (gtk_widget_get_realized(top)) return FALSE;
  GtkWidget *bar = gtk_header_bar_new();
  gtk_header_bar_set_title(GTK_HEADER_BAR(bar), title);
  gtk_header_bar_set_show_close_button(GTK_HEADER_BAR(bar), TRUE);
  GtkStyleContext *style = gtk_widget_get_style_context(bar);
  gtk_style_context_add_class(style, "default-decoration"); // the theme's compact bar
  gtk_style_context_add_class(style, "studio-bar");
  gtk_style_context_add_class(gtk_widget_get_style_context(top), "studio-window");
  gtk_widget_show(bar);
  gtk_window_set_titlebar(GTK_WINDOW(top), bar);
  return FALSE;
}

static void bar_watch(const char *title) {
  // The shell's file is named for the WebKit it links, and GTK takes the
  // window's class from it; desktop files and taskbars match the app's name.
  // WebKit names its cache and storage folders from this too (uninstall.sh).
  g_set_prgname("activate-studio");
  gdk_set_program_class("activate-studio");
  g_type_class_ref(GTK_TYPE_WIDGET);
  g_signal_add_emission_hook(g_signal_lookup("show", GTK_TYPE_WIDGET), 0, bar_attach, g_strdup(title), g_free);
}
*/
import "C"

import "unsafe"

// useOwnTitleBar gives the window a title bar it draws itself, since one the
// window manager draws cannot take the page's color. A GtkHeaderBar, and not
// GTK_CSD=1, which GTK ignores where no compositor runs, as in many remote
// desktop sessions. Wails makes the window inside wails.Run and offers no
// call before showing it, so this watches for it. Call before wails.Run.
func useOwnTitleBar(title string) {
	c := C.CString(title)
	defer C.free(unsafe.Pointer(c))
	C.bar_watch(c)
}

// titleInsetPx: the Linux window draws its own title bar (useOwnTitleBar).
const titleInsetPx = 0

// paintBar styles the bar in the page's #rrggbb (barCSS).
func paintBar(color string) {
	c := C.CString(barCSS(color))
	defer C.free(unsafe.Pointer(c))
	C.bar_set(c)
}
