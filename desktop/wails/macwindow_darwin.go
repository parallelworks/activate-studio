//go:build darwin

package main

// What the Swift window did with its NSWindow and WKWebView beyond the
// title bar (titlebar_darwin.go), on the window Wails makes:
//   - the frame is kept under the Swift window's autosave name, so the
//     size and place carry over from it and from one launch to the next;
//   - Hide Others;
//   - the microphone for voice input is granted to the Studio's own page
//     (127.0.0.1) once macOS has asked the person, and refused elsewhere;
//   - an uninstall stops the window keeping its frame, and with the data
//     chosen clears its web data and preferences.

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#import <objc/runtime.h>

static NSString *const frameName = @"ACTIVATE Studio";
static BOOL watching;

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

static void adopt(NSWindow *w) {
	if ([w isKindOfClass:[NSPanel class]] || !(w.styleMask & NSWindowStyleMaskTitled)) return;
	if (w.frameAutosaveName.length == 0 && ![[w identifier] isEqualToString:@"studio-forgotten"]) {
		[w setFrameUsingName:frameName];
		[w setFrameAutosaveName:frameName];
	}
	WKWebView *web = findWebView(w.contentView);
	if (web) allowMicrophone(web);
}

static void mac_window_watch(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		if (watching) return;
		watching = YES;
		[[NSNotificationCenter defaultCenter] addObserverForName:NSWindowDidBecomeKeyNotification
			object:nil queue:[NSOperationQueue mainQueue] usingBlock:^(NSNotification *n) { adopt(n.object); }];
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
*/
import "C"

// watchMacWindow adopts each window as it becomes key. Call before wails.Run.
func watchMacWindow() { C.mac_window_watch() }

func hideOthers() { C.mac_hide_others() }

// forgetMacWindow: see mac_forget_window. Not on the main thread.
func forgetMacWindow(purge bool) {
	p := C.int(0)
	if purge {
		p = 1
	}
	C.mac_forget_window(p)
}
