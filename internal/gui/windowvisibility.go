//go:build !nogui

package gui

import (
	"runtime"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// A macOS window hidden with orderOut keeps its WKWebView's page visible, so
// tell the page before hiding it and tell it again when it is shown. Only
// the Mac is told: WebView2 and WebKitGTK mark a hidden window's page hidden
// themselves, and there the panel is also shown by Wails' ToggleWindow,
// which would never tell the page it is back (#4293).
const (
	pageHiddenJS = "window.magpieWindowVisibility?.(false)"
	pageShownJS  = "window.magpieWindowVisibility?.(true)"
)

func hideWebview(w *application.WebviewWindow) {
	if w == nil {
		return
	}
	if js := pageHiddenJSOn(runtime.GOOS); js != "" {
		w.ExecJS(js)
	}
	w.Hide()
}

func showWebview(w *application.WebviewWindow) *application.WebviewWindow {
	if w == nil {
		return nil
	}
	w.Show()
	w.ExecJS(pageShownJS)
	return w
}

// sayWailsHides tells the panel's page when Wails hides the panel itself:
// on a click elsewhere (HideOnFocusLost), the way it is mostly closed, a
// plain Hide that hideWebview never sees, and on Escape, whose HideOnEscape
// binding is replaced here by hideWebview.
// A page never told it was hidden isn't told it is back either, so it kept
// the settings it was drawn with: Settings' ¥ and 万 / 亿 never reached the
// panel's Usage tab (blrbrzkj on Discord).
//
// The page is told from a hook, which Wails runs before any listener, so the
// signal is sent to it before HideOnFocusLost's Hide orders the panel out
// (WebKit takes the two in the order sent). Wails runs each listener in a
// goroutine of its own: as a listener, the Hide could come first, WebKit
// (on a Mac whose screen is on) said hidden with the page not marked, and a
// late signal could reach the page after the panel was shown again, which
// left it marked out of sight while on screen.
func sayWailsHides(w *application.WebviewWindow) {
	if js := pageHiddenJSOn(runtime.GOOS); js != "" {
		w.RegisterHook(events.Common.WindowLostFocus, func(*application.WindowEvent) { w.ExecJS(js) })
	}
	w.RegisterKeyBinding("escape", func(application.Window) { hideWebview(w) })
}

// pageHiddenJSOn is what a page on goos is told as its window hides.
func pageHiddenJSOn(goos string) string {
	if goos != "darwin" {
		return ""
	}
	return pageHiddenJS
}
