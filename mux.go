// SPDX-License-Identifier: MIT
//
// Copyright 2021 Andrew Bursavich. All rights reserved.
// Use of this source code is governed by The MIT License
// which can be found in the LICENSE file.

// Package httpprom provides prometheus metrics for HTTP servers.
package httpprom

import (
	"net/http"

	"bursavich.dev/httpprom/internal/forked/prometheus/promhttp"
	"github.com/prometheus/client_golang/prometheus"
)

// A Option changes the default behavior.
type Option interface {
	applyMiddlewareOpt(*Middleware)
}

type muxOptFunc func(*Middleware)

func (fn muxOptFunc) applyMiddlewareOpt(mw *Middleware) { fn(mw) }

// WithCode returns a mux option that adds a status code label to metrics.
func WithCode() Option {
	return muxOptFunc(func(mw *Middleware) { mw.code = true })
}

// WithMethod returns a mux option that adds a method label to metrics.
func WithMethod() Option {
	return muxOptFunc(func(mw *Middleware) { mw.method = true })
}

// WithNamespace returns a mux option that adds a namespace to all metrics.
func WithNamespace(namespace string) Option {
	return muxOptFunc(func(mw *Middleware) { mw.namespace = namespace })
}

// WithConstLabels returns a mux option that adds constant labels to all metrics.
// Metrics with the same fully-qualified name must have the same label names in
// their ConstLabels.
func WithConstLabels(labels prometheus.Labels) Option {
	return muxOptFunc(func(mw *Middleware) { mw.constLabels = labels })
}

type beforeFunc func(handler, method string)
type afterFunc func(handler, method, code string)

type handlerConfig struct {
	name          string
	handler       http.Handler
	pendingBefore beforeFunc
	pendingDefer  beforeFunc
	requestAfter  afterFunc
}

func (h *handlerConfig) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method := lookupMethod(r.Method)
	h.pendingBefore(h.name, method)
	defer h.pendingDefer(h.name, method)

	d := promhttp.NewDelegator(w)
	h.handler.ServeHTTP(d, r)

	code := lookupCode(d.Status())
	h.requestAfter(h.name, method, code)
}

// Middleware wraps and instruments http handlers.
type Middleware struct {
	requests *prometheus.GaugeVec
	pending  *prometheus.GaugeVec

	namespace   string
	constLabels prometheus.Labels
	method      bool
	code        bool
}

// NewMiddleware returns new Middleware with the given options.
func NewMiddleware(options ...Option) *Middleware {
	var mw Middleware
	mw.init(options)
	return &mw
}

func (mw *Middleware) init(options []Option) {
	for _, opt := range options {
		opt.applyMiddlewareOpt(mw)
	}
	mw.requests = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name:        "http_server_requests_total",
		Help:        "Total number of HTTP server requests completed.",
		Namespace:   mw.namespace,
		ConstLabels: mw.constLabels,
	}, coalesce("handler", maybe("method", mw.method), maybe("code", mw.code)))
	mw.pending = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name:        "http_server_requests_pending",
		Help:        "Number of HTTP server requests currently pending.",
		Namespace:   mw.namespace,
		ConstLabels: mw.constLabels,
	}, coalesce("handler", maybe("method", mw.method)))
}

// Collector returns a prometheus collector for the metrics.
func (mw *Middleware) Collector() prometheus.Collector {
	return collectors{mw.requests, mw.pending}
}

// Handler returns an instrumented handler with the given name.
func (mw *Middleware) Handler(name string, handler http.Handler) http.Handler {
	return mw.handler(name, handler)
}

// HandlerFunc returns an instrumented handler with the given name.
func (mw *Middleware) HandlerFunc(name string, handler http.HandlerFunc) http.Handler {
	return mw.handler(name, handler)
}

func (mw *Middleware) handler(name string, handler http.Handler) *handlerConfig {
	if handler == nil {
		panic("promhttp: nil handler")
	}
	return &handlerConfig{
		name:          name,
		handler:       handler,
		pendingBefore: mw.pendingBeforeFunc(),
		pendingDefer:  mw.pendingDeferFunc(),
		requestAfter:  mw.requestsAfterFunc(),
	}
}

func (mw *Middleware) pendingBeforeFunc() beforeFunc {
	if mw.method {
		return func(handler, method string) {
			mw.pending.WithLabelValues(handler, method).Inc()
		}
	}
	return func(handler, method string) {
		mw.pending.WithLabelValues(handler).Inc()
	}
}

func (mw *Middleware) pendingDeferFunc() beforeFunc {
	switch {
	case mw.method:
		return func(handler, method string) {
			mw.pending.WithLabelValues(handler, method).Dec()
		}
	default:
		return func(handler, method string) {
			mw.pending.WithLabelValues(handler).Dec()
		}
	}
}

func (mw *Middleware) requestsAfterFunc() afterFunc {
	switch {
	case mw.method && mw.code:
		return func(handler, method, code string) {
			mw.requests.WithLabelValues(handler, method, code).Inc()
		}
	case mw.method:
		return func(handler, method, code string) {
			mw.requests.WithLabelValues(handler, method).Inc()
		}
	case mw.code:
		return func(handler, method, code string) {
			mw.requests.WithLabelValues(handler, code).Inc()
		}
	default:
		return func(handler, method, code string) {
			mw.requests.WithLabelValues(handler).Inc()
		}
	}
}

// ServeMux is an HTTP request multiplexer that wraps handlers with
// prometheus instrumentation middleware.
type ServeMux struct {
	Middleware

	mux http.ServeMux
}

// NewServeMux returns a new mux with the given options.
func NewServeMux(options ...Option) *ServeMux {
	var mux ServeMux
	mux.Middleware.init(options)
	return &mux
}

// ServeHTTP dispatches the request to the handler whose
// pattern most closely matches the request URL.
func (mux *ServeMux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	mux.mux.ServeHTTP(w, r)
}

// Handle registers the handler for the given pattern.
// It panics if a handler already exists for pattern.
func (mux *ServeMux) Handle(pattern string, handler http.Handler) {
	mux.mux.Handle(pattern, mux.Middleware.handler(pattern, handler))
}

// HandleFunc registers the handler function for the given pattern.
// It panics if a handler already exists for pattern.
func (mux *ServeMux) HandleFunc(pattern string, handler http.HandlerFunc) {
	mux.mux.Handle(pattern, mux.Middleware.handler(pattern, handler))
}

type collectors []prometheus.Collector

func (cs collectors) Describe(ch chan<- *prometheus.Desc) {
	for _, c := range cs {
		c.Describe(ch)
	}
}

func (cs collectors) Collect(ch chan<- prometheus.Metric) {
	for _, c := range cs {
		c.Collect(ch)
	}
}

func coalesce(labels ...string) []string {
	for i := 0; i < len(labels); {
		if labels[i] == "" {
			copy(labels[i:], labels[i+1:])  // shift rest back one
			labels = labels[:len(labels)-1] // chop off last elem
			continue
		}
		i++
	}
	return labels
}

func maybe(label string, yes bool) string {
	if yes {
		return label
	}
	return ""
}
