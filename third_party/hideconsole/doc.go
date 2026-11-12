// Package hideconsole stands in for github.com/ebitengine/hideconsole,
// which Ebitengine imports: that one closes, as the program starts, the
// console Windows opened for it (a double click in Explorer). Say starts in
// that console: it asks there whether to open its window or stay in the
// terminal, and stays there when the terminal is chosen. So this one does
// nothing; Say closes the console itself when it opens the window
// (hideConsole in relaunch_windows.go).
package hideconsole
