// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package profile

import (
	"net/http"
	"net/http/pprof"
)

var (
	PprofHandlers = map[string]http.Handler{
		"/debug/pprof/":             http.HandlerFunc(pprof.Index),
		"/debug/pprof/cmdline":      http.HandlerFunc(pprof.Cmdline),
		"/debug/pprof/profile":      http.HandlerFunc(pprof.Profile),
		"/debug/pprof/symbol":       http.HandlerFunc(pprof.Symbol),
		"/debug/pprof/trace":        http.HandlerFunc(pprof.Trace),
		"/debug/pprof/goroutine":    pprof.Handler("goroutine"),
		"/debug/pprof/heap":         pprof.Handler("heap"),
		"/debug/pprof/threadcreate": pprof.Handler("threadcreate"),
		"/debug/pprof/block":        pprof.Handler("block"),
		"/debug/pprof/allocs":       pprof.Handler("allocs"),
	}
)
