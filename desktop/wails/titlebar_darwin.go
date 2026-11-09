//go:build darwin

package main

// The macOS bar, as the Swift window painted it (paintBar in
// deploy/mac/shell/StudioShell.swift): a transparent title bar shows the
// window's background, so the window takes the page's color, and the title
// and window controls take the light or dark appearance that reads on it.
// Wails makes its window inside wails.Run, so a color set before then waits
// for the window to become key, and each window is painted as it does.

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

static NSColor *barColor;
static BOOL barLightInk;
static BOOL barWatching;

static void bar_apply(NSWindow *w) {
	if (barColor == nil || [w isKindOfClass:[NSPanel class]] || !(w.styleMask & NSWindowStyleMaskTitled)) return;
	w.titlebarAppearsTransparent = YES;
	if (@available(macOS 11.0, *)) w.titlebarSeparatorStyle = NSTitlebarSeparatorStyleNone;
	w.backgroundColor = barColor;
	NSView *bar = [w standardWindowButton:NSWindowCloseButton].superview;
	bar.appearance = [NSAppearance appearanceNamed:barLightInk ? NSAppearanceNameDarkAqua : NSAppearanceNameAqua];
}

// The main window's toolbar row: none in the compact layout, so the window
// controls sit at a plain title bar's height; otherwise an empty toolbar in
// the unified style, which puts them in a 52px row (macOptions).
static void bar_set_compact(int compact) {
	dispatch_async(dispatch_get_main_queue(), ^{
		for (NSWindow *w in [NSApp windows]) {
			if ([w isKindOfClass:[NSPanel class]] || !(w.styleMask & NSWindowStyleMaskTitled)) continue;
			if ([[w identifier] isEqualToString:@"studio-popup"]) continue;
			if (compact) {
				w.toolbar = nil;
			} else if (w.toolbar == nil) {
				NSToolbar *t = [[NSToolbar alloc] initWithIdentifier:@"wails.toolbar"];
				t.showsBaselineSeparator = NO;
				w.toolbar = t;
				w.toolbarStyle = NSWindowToolbarStyleUnified;
			}
		}
	});
}

static void bar_paint(double r, double g, double b, int lightInk) {
	dispatch_async(dispatch_get_main_queue(), ^{
		barColor = [NSColor colorWithSRGBRed:r green:g blue:b alpha:1];
		barLightInk = lightInk != 0;
		if (!barWatching) {
			barWatching = YES;
			[[NSNotificationCenter defaultCenter] addObserverForName:NSWindowDidBecomeKeyNotification
				object:nil queue:[NSOperationQueue mainQueue] usingBlock:^(NSNotification *n) { bar_apply(n.object); }];
		}
		for (NSWindow *w in [NSApp windows]) bar_apply(w);
	});
}
*/
import "C"

import "strconv"

// titleInsetPx is the height of the window's top row, where the window
// controls sit: the window has no title bar of its own, and its title bar
// is a unified toolbar row this tall (macOptions, macwindow_darwin.go), with
// the controls centered in it. The page puts each column's first row on it
// (html.shell-inset), and the shim makes its empty parts drag the window.
const titleInsetPx = 52

// titleControlsPx is how far in from the window's left edge the window
// controls reach, with a gap after them: the page starts its top row's
// content past it where the controls sit over that row. In the unified
// toolbar row the three controls' frames are 14 by 16 at x 19, 39 and 59,
// y 18 (the self test's window report), so they end at 73.
const titleControlsPx = 82

// compactInsetPx and compactControlsPx are the same for the compact layout,
// with a classification banner (setCompactTitle): no toolbar, so the
// controls sit at a plain title bar's height, and the banner is that strip.
const (
	compactInsetPx    = 28
	compactControlsPx = 76
)

// titleMetrics is the row's height and the controls' reach in a layout.
func titleMetrics(compact bool) (int, int) {
	if compact {
		return compactInsetPx, compactControlsPx
	}
	return titleInsetPx, titleControlsPx
}

// setWindowCompact adds or removes the window's toolbar row
// (macwindow_darwin.go).
func setWindowCompact(compact bool) {
	c := C.int(0)
	if compact {
		c = 1
	}
	C.bar_set_compact(c)
}

// useOwnTitleBar: the macOS bar is the system's, painted by paintBar.
func useOwnTitleBar(string) {}

// paintBar gives the window the page's #rrggbb (checked by setBarColor).
func paintBar(color string) {
	ch := func(at int) C.double {
		v, _ := strconv.ParseUint(color[at:at+2], 16, 8)
		return C.double(float64(v) / 255)
	}
	light := C.int(0)
	if barInk(color) == "#ffffff" {
		light = 1
	}
	C.bar_paint(ch(1), ch(3), ch(5), light)
}
