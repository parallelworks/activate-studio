//go:build linux

package main

// The window's zoom on Linux (zoom.go): WebKitGTK's zoom level on the
// window's web view, found among GTK's windows by its type name, so only
// GTK is linked here, as with either WebKitGTK API. The page takes Ctrl+=,
// Ctrl+- and Ctrl+0 (shell/shim.js), since the window has no menu.

/*
#cgo pkg-config: gtk+-3.0
#include <stdlib.h>
#include <string.h>
#include <gtk/gtk.h>

static void zoom_set_on(GtkWidget *w, gpointer z) {
	if (strcmp(G_OBJECT_TYPE_NAME(w), "WebKitWebView") == 0) {
		g_object_set(G_OBJECT(w), "zoom-level", *(gdouble *)z, NULL);
		return;
	}
	if (GTK_IS_CONTAINER(w)) gtk_container_forall(GTK_CONTAINER(w), zoom_set_on, z);
}

static gboolean zoom_apply(gpointer z) {
	GList *tops = gtk_window_list_toplevels();
	for (GList *l = tops; l != NULL; l = l->next) zoom_set_on(GTK_WIDGET(l->data), z);
	g_list_free(tops);
	g_free(z);
	return G_SOURCE_REMOVE;
}

// GTK's own thread applies it.
static void zoom_set(double z) {
	gdouble *p = g_new(gdouble, 1);
	*p = z;
	g_idle_add_full(G_PRIORITY_HIGH, zoom_apply, p, NULL);
}
*/
import "C"

import "fmt"

const pageZoomWorks = true
const zoomKeys = true

func initPageZoom(float64) {}

// setPageZoom: the page applies the saved level once it has loaded (the
// shim asks with Zoom("")), so a launch needs nothing before the window.
func (a *App) setPageZoom(z float64) {
	C.zoom_set(C.double(z))
	a.execJS(fmt.Sprintf("window.__studioShell && window.__studioShell.setZoom && window.__studioShell.setZoom(%g)", z))
}
