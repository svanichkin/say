package contacts

import (
	"strings"

	"github.com/fsnotify/fsnotify"
)

// watchFolder tells changed whenever a card in dir is written, made,
// removed or renamed, by whatever program: the system's notifications
// (FSEvents/kqueue on macOS and the BSDs, inotify on Linux,
// ReadDirectoryChangesW on Windows; none on Plan 9: an error, and the book
// changes there only by Say's own writes).
func watchFolder(dir string, changed func()) (stop func(), err error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return func() {}, err
	}
	if err := w.Add(dir); err != nil {
		w.Close()
		return func() {}, err
	}
	go func() {
		for {
			select {
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				if strings.HasSuffix(strings.ToLower(ev.Name), ".vcf") {
					go changed() // never holding up the watcher (changed takes the store's lock)
				}
			case _, ok := <-w.Errors:
				if !ok {
					return
				}
			}
		}
	}()
	return func() { w.Close() }, nil
}
