//go:build darwin

package main

// What the Swift window did with its NSWindow and WKWebView beyond the
// title bar (titlebar_darwin.go), on the window Wails makes:
//   - the frame is kept under the Swift window's autosave name, so the
//     size and place carry over from it and from one launch to the next;
//   - Hide Others;
//   - the microphone for voice input is granted to the Studio's own page
//     (127.0.0.1) once macOS has asked the person, and refused elsewhere;
//   - window.open and target=_blank: the Studio's own pages (a session
//     popped out) get a window of their own, anything else opens in the
//     browser;
//   - an uninstall stops the window keeping its frame, and with the data
//     chosen clears its web data and preferences.

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#import <objc/runtime.h>
#include <stdlib.h>

static NSString *const frameName = @"ACTIVATE Studio";
static NSString *const popupID = @"studio-popup";
static BOOL watching;
static NSMutableArray<NSWindow *> *popups;
// The page zoom every Studio window's web view takes (zoom.go); main
// thread only once the app runs.
static double pageZoom = 1;

static WKWebView *findWebView(NSView *v) {
	if ([v isKindOfClass:[WKWebView class]]) return (WKWebView *)v;
	for (NSView *c in v.subviews) {
		WKWebView *w = findWebView(c);
		if (w) return w;
	}
	return nil;
}

// The UI delegate Wails gives its web view answers no media request, so
// the answer is added to its class: the Studio's page may use the
// microphone, nothing else may capture anything.
static void allowMicrophone(WKWebView *web) {
	if (@available(macOS 12.0, *)) {
		id d = web.UIDelegate;
		SEL sel = @selector(webView:requestMediaCapturePermissionForOrigin:initiatedByFrame:type:decisionHandler:);
		if (d == nil || [d respondsToSelector:sel]) return;
		IMP imp = imp_implementationWithBlock(^(id self_, WKWebView *wv, WKSecurityOrigin *origin, WKFrameInfo *frame,
		                                         WKMediaCaptureType type, void (^decide)(WKPermissionDecision)) {
			decide([origin.host isEqualToString:@"127.0.0.1"] && type == WKMediaCaptureTypeMicrophone
			           ? WKPermissionDecisionGrant : WKPermissionDecisionDeny);
		});
		class_addMethod([d class], sel, imp, "v@:@@@q@?");
	}
}

static BOOL isStudio(NSURL *url) {
	if (url == nil) return YES;
	if ([@[@"about", @"blob"] containsObject:(url.scheme ?: @"")]) return YES;
	return [url.host isEqualToString:@"127.0.0.1"];
}

// The Swift window's createWebViewWith: a window of 1100 by 760 cascaded
// from the main one, whose web view takes WebKit's configuration (it must)
// and the main view's UI delegate, so its own pop-ups, dialogs and voice
// input work as in the main window.
static void allowPopups(WKWebView *web) {
	id d = web.UIDelegate;
	if (d == nil) return;
	SEL create = @selector(webView:createWebViewWithConfiguration:forNavigationAction:windowFeatures:);
	if (![d respondsToSelector:create]) {
		IMP imp = imp_implementationWithBlock(^WKWebView *(id self_, WKWebView *wv, WKWebViewConfiguration *cfg,
		                                                   WKNavigationAction *action, WKWindowFeatures *features) {
			NSURL *url = action.request.URL;
			if (!isStudio(url)) {
				[[NSWorkspace sharedWorkspace] openURL:url];
				return nil;
			}
			WKWebView *child = [[WKWebView alloc] initWithFrame:NSZeroRect configuration:cfg];
			child.UIDelegate = wv.UIDelegate;
			child.pageZoom = pageZoom;
			NSWindow *w = [[NSWindow alloc] initWithContentRect:NSMakeRect(0, 0, 1100, 760)
				styleMask:NSWindowStyleMaskTitled | NSWindowStyleMaskClosable | NSWindowStyleMaskMiniaturizable | NSWindowStyleMaskResizable
				backing:NSBackingStoreBuffered defer:NO];
			w.identifier = popupID;
			w.releasedWhenClosed = NO;
			w.title = @"ACTIVATE Studio";
			w.contentView = child;
			NSWindow *main = wv.window;
			if (main) [w cascadeTopLeftFromPoint:NSMakePoint(NSMinX(main.frame), NSMaxY(main.frame))];
			else [w center];
			if (popups == nil) popups = [NSMutableArray array];
			[popups addObject:w];
			[w makeKeyAndOrderFront:nil];
			return child;
		});
		class_addMethod([d class], create, imp, "@@:@@@@");
	}
	SEL close = @selector(webViewDidClose:);
	if (![d respondsToSelector:close]) {
		IMP imp = imp_implementationWithBlock(^(id self_, WKWebView *wv) {
			for (NSWindow *w in [popups copy]) {
				if (w.contentView == wv) [w close];
			}
		});
		class_addMethod([d class], close, imp, "v@:@");
	}
}

static void adopt(NSWindow *w) {
	if ([w isKindOfClass:[NSPanel class]] || !(w.styleMask & NSWindowStyleMaskTitled)) return;
	BOOL popup = [[w identifier] isEqualToString:popupID];
	if (!popup && w.frameAutosaveName.length == 0 && ![[w identifier] isEqualToString:@"studio-forgotten"]) {
		[w setFrameUsingName:frameName];
		[w setFrameAutosaveName:frameName];
		// AppKit writes the frame on the next move or resize, or not until
		// then: write it now, so a window never moved still opens there.
		[w saveFrameUsingName:frameName];
	}
	// The main window's title bar is a toolbar row (TitleBarHiddenInset in
	// macOptions) in the unified style, so the window controls sit lower and
	// further in, centered on the page's first row (titleInsetPx).
	if (!popup && w.toolbar != nil) w.toolbarStyle = NSWindowToolbarStyleUnified;
	WKWebView *web = findWebView(w.contentView);
	if (web) {
		allowMicrophone(web);
		allowPopups(web);
		if (web.pageZoom != pageZoom) web.pageZoom = pageZoom;
	}
}

// mac_set_zoom gives every Studio window's web view the page zoom z and
// tells each page's shim (shell/shim.js), whose title bar row and file
// drops are in the window's points.
static void mac_set_zoom(double z) {
	dispatch_async(dispatch_get_main_queue(), ^{
		pageZoom = z;
		NSString *js = [NSString stringWithFormat:@"window.__studioShell && window.__studioShell.setZoom && window.__studioShell.setZoom(%g)", z];
		for (NSWindow *w in [NSApp windows]) {
			WKWebView *web = findWebView(w.contentView);
			if (web == nil) continue;
			web.pageZoom = z;
			[web evaluateJavaScript:js completionHandler:nil];
		}
	});
}

// mac_init_zoom sets the level the windows open at, before the app runs.
static void mac_init_zoom(double z) { pageZoom = z; }

static void mac_window_watch(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		if (watching) return;
		watching = YES;
		[[NSNotificationCenter defaultCenter] addObserverForName:NSWindowDidBecomeKeyNotification
			object:nil queue:[NSOperationQueue mainQueue] usingBlock:^(NSNotification *n) { adopt(n.object); }];
		// A closed pop-up window is let go.
		[[NSNotificationCenter defaultCenter] addObserverForName:NSWindowWillCloseNotification
			object:nil queue:[NSOperationQueue mainQueue] usingBlock:^(NSNotification *n) { [popups removeObject:n.object]; }];
		for (NSWindow *w in [NSApp windows]) adopt(w);
	});
}

static void mac_hide_others(void) {
	dispatch_async(dispatch_get_main_queue(), ^{ [NSApp hideOtherApplications:nil]; });
}

// mac_forget_window runs before the launcher's uninstall: the window stops
// keeping its frame and state and, with purge, its web data and the app's
// preferences are cleared. It waits for that; call it off the main thread.
static void mac_forget_window(int purge) {
	dispatch_semaphore_t done = dispatch_semaphore_create(0);
	dispatch_async(dispatch_get_main_queue(), ^{
		for (NSWindow *w in [NSApp windows]) {
			w.restorable = NO;
			[w setFrameAutosaveName:@""];
			w.identifier = @"studio-forgotten";
		}
		[NSWindow removeFrameUsingName:frameName];
		if (!purge) { dispatch_semaphore_signal(done); return; }
		NSString *id_ = [[NSBundle mainBundle] bundleIdentifier];
		if (id_) [[NSUserDefaults standardUserDefaults] removePersistentDomainForName:id_];
		[[WKWebsiteDataStore defaultDataStore] removeDataOfTypes:[WKWebsiteDataStore allWebsiteDataTypes]
			modifiedSince:[NSDate distantPast] completionHandler:^{ dispatch_semaphore_signal(done); }];
	});
	dispatch_semaphore_wait(done, dispatch_time(DISPATCH_TIME_NOW, 10 * NSEC_PER_SEC));
}
// mac_window_report describes the main window as the self test sees it,
// as JSON (the caller frees it). Call off the main thread.
static char *mac_window_report(void) {
	__block NSString *json = @"{}";
	dispatch_sync(dispatch_get_main_queue(), ^{
		NSWindow *main = nil;
		NSUInteger n = 0;
		for (NSWindow *w in [NSApp windows]) {
			if ([w isKindOfClass:[NSPanel class]] || !(w.styleMask & NSWindowStyleMaskTitled)) continue;
			n++;
			if (main == nil && ![[w identifier] isEqualToString:popupID]) main = w;
		}
		if (main == nil) return;
		NSColor *bg = [main.backgroundColor colorUsingColorSpace:[NSColorSpace sRGBColorSpace]];
		NSString *hex = bg ? [NSString stringWithFormat:@"#%02x%02x%02x", (int)lround(bg.redComponent * 255),
			(int)lround(bg.greenComponent * 255), (int)lround(bg.blueComponent * 255)] : @"";
		NSView *bar = [main standardWindowButton:NSWindowCloseButton].superview;
		WKWebView *web = findWebView(main.contentView);
		id d = web.UIDelegate;
		// The window controls in page coordinates (top left origin), and
		// the window's number for screencapture -l.
		NSMutableArray *controls = [NSMutableArray array];
		CGFloat top = main.contentView.bounds.size.height;
		for (NSNumber *k in @[@(NSWindowCloseButton), @(NSWindowMiniaturizeButton), @(NSWindowZoomButton)]) {
			NSButton *b = [main standardWindowButton:(NSWindowButton)k.integerValue];
			if (b == nil) continue;
			NSRect f = [b convertRect:b.bounds toView:nil];
			[controls addObject:@{@"x": @(f.origin.x), @"y": @(top - NSMaxY(f)), @"w": @(f.size.width), @"h": @(f.size.height)}];
		}
		NSDictionary *r = @{
			@"windowNumber": @(main.windowNumber),
			@"controls": controls,
			@"titlebarHeight": @(main.frame.size.height - main.contentLayoutRect.size.height),
			@"toolbarStyle": @((NSInteger)main.toolbarStyle),
			@"windows": @(n),
			@"titlebarTransparent": @(main.titlebarAppearsTransparent),
			@"background": hex,
			@"barAppearance": bar.appearance.name ?: @"",
			@"frameAutosaveName": main.frameAutosaveName ?: @"",
			@"frameSaved": @((BOOL)([[NSUserDefaults standardUserDefaults] objectForKey:[@"NSWindow Frame " stringByAppendingString:frameName]] != nil)),
			@"microphoneHandler": @([d respondsToSelector:@selector(webView:requestMediaCapturePermissionForOrigin:initiatedByFrame:type:decisionHandler:)]),
			@"popupHandler": @([d respondsToSelector:@selector(webView:createWebViewWithConfiguration:forNavigationAction:windowFeatures:)]),
			@"closeHandler": @([d respondsToSelector:@selector(webViewDidClose:)]),
			@"pageZoom": @(web ? web.pageZoom : 0),
		};
		NSData *data = [NSJSONSerialization dataWithJSONObject:r options:0 error:nil];
		if (data) json = [[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding];
	});
	return strdup(json.UTF8String);
}
*/
import "C"

import (
	"encoding/json"
	"unsafe"
)

// watchMacWindow adopts each window as it becomes key. Call before wails.Run.
func watchMacWindow() { C.mac_window_watch() }

func hideOthers() { C.mac_hide_others() }

// The window's zoom is WKWebView.pageZoom (zoom.go); the menu has the keys.
const pageZoomWorks = true
const zoomKeys = false

func initPageZoom(z float64)         { C.mac_init_zoom(C.double(z)) }
func (a *App) setPageZoom(z float64) { C.mac_set_zoom(C.double(z)) }

// forgetMacWindow: see mac_forget_window. Not on the main thread.
func forgetMacWindow(purge bool) {
	p := C.int(0)
	if purge {
		p = 1
	}
	C.mac_forget_window(p)
}

// windowReport is the main window's native state for the self test.
func windowReport() map[string]any {
	c := C.mac_window_report()
	defer C.free(unsafe.Pointer(c))
	var r map[string]any
	_ = json.Unmarshal([]byte(C.GoString(c)), &r)
	return r
}
