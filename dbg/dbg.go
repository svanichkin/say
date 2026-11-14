// Package dbg says whether this is a debug build: go build -tags debug
// (make debug). A debug build traces calls in detail ([debug] and [trace]
// lines, logged with -log debug), keeps its log to send to a peer that asks
// for it over a call, and on Windows logs at debug level without flags. A
// release build (go build, make build, the releases) does none of it: the
// tracing costs a little CPU, its log grows by tens of MB an hour, and the
// log sent over a call takes bandwidth from it and shows the peer our
// addresses.
package dbg
