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

// A ServeMuxOption changes the default behavior of a mux.
type ServeMuxOption interface {
	applyMuxOpt(*ServeMux)
}

type muxOptFunc func(*ServeMux)

func (fn muxOptFunc) applyMuxOpt(c *ServeMux) { fn(c) }

// WithCode returns a mux option that adds a status code label to metrics.
func WithCode() ServeMuxOption {
	return muxOptFunc(func(mux *ServeMux) { mux.code = true })
}

// WithMethod returns a mux option that adds a method label to metrics.
func WithMethod() ServeMuxOption {
	return muxOptFunc(func(mux *ServeMux) { mux.method = true })
}

// WithNamespace returns a mux option that adds a namespace to all metrics.
func WithNamespace(namespace string) ServeMuxOption {
	return muxOptFunc(func(mux *ServeMux) { mux.namespace = namespace })
}

// WithConstLabels returns a mux option that adds constant labels to all metrics.
// Metrics with the same fully-qualified name must have the same label names in
// their ConstLabels.
func WithConstLabels(labels prometheus.Labels) ServeMuxOption {
	return muxOptFunc(func(mux *ServeMux) { mux.constLabels = labels })
}

// A HandlerOption changes the default behavior of a handler.
type HandlerOption interface {
	applyHandlerOpt(*handlerConfig)
}

type handlerOptFunc func(*handlerConfig)

func (fn handlerOptFunc) applyHandlerOpt(c *handlerConfig) { fn(c) }

// WithName returns a handler option that sets the name of the handler.
// The default value is the handler pattern.
func WithName(name string) HandlerOption {
	return handlerOptFunc(func(c *handlerConfig) { c.name = name })
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

// ServeMux is an HTTP request multiplexer that wraps handlers with
// prometheus instrumentation middleware.
type ServeMux struct {
	mux http.ServeMux

	requests *prometheus.GaugeVec
	pending  *prometheus.GaugeVec

	namespace   string
	constLabels prometheus.Labels
	method      bool
	code        bool
}

// NewServeMux returns a new mux with the given options.
func NewServeMux(options ...ServeMuxOption) *ServeMux {
	var mux ServeMux
	for _, opt := range options {
		opt.applyMuxOpt(&mux)
	}
	mux.requests = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name:        "http_server_requests_total",
		Help:        "Total number of HTTP server requests completed.",
		Namespace:   mux.namespace,
		ConstLabels: mux.constLabels,
	}, coalesce("handler", maybe("method", mux.method), maybe("code", mux.code)))
	mux.pending = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name:        "http_server_requests_pending",
		Help:        "Number of HTTP server requests currently pending.",
		Namespace:   mux.namespace,
		ConstLabels: mux.constLabels,
	}, coalesce("handler", maybe("method", mux.method)))
	return &mux
}

// Collector returns a prometheus collector for the mux's metrics.
func (mux *ServeMux) Collector() prometheus.Collector {
	return collectors{mux.requests, mux.pending}
}

// ServeHTTP dispatches the request to the handler whose
// pattern most closely matches the request URL.
func (mux *ServeMux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	mux.mux.ServeHTTP(w, r)
}

// Handle registers the handler for the given pattern.
// It panics if a handler already exists for pattern.
func (mux *ServeMux) Handle(pattern string, handler http.Handler, options ...HandlerOption) {
	if handler == nil {
		panic("promhttp: nil handler")
	}
	cfg := &handlerConfig{
		name:          pattern,
		handler:       handler,
		pendingBefore: mux.pendingBeforeFunc(),
		pendingDefer:  mux.pendingDeferFunc(),
		requestAfter:  mux.requestsAfterFunc(),
	}
	for _, opt := range options {
		opt.applyHandlerOpt(cfg)
	}
	mux.mux.Handle(pattern, cfg)
}

// HandleFunc registers the handler function for the given pattern.
// It panics if a handler already exists for pattern.
func (mux *ServeMux) HandleFunc(pattern string, handler http.HandlerFunc, options ...HandlerOption) {
	if handler == nil {
		panic("promhttp: nil handler")
	}
	mux.Handle(pattern, handler, options...)
}

func (mux *ServeMux) pendingBeforeFunc() beforeFunc {
	if mux.method {
		return func(handler, method string) {
			mux.pending.WithLabelValues(handler, method).Inc()
		}
	}
	return func(handler, method string) {
		mux.pending.WithLabelValues(handler).Inc()
	}
}

func (mux *ServeMux) pendingDeferFunc() beforeFunc {
	switch {
	case mux.method:
		return func(handler, method string) {
			mux.pending.WithLabelValues(handler, method).Dec()
		}
	default:
		return func(handler, method string) {
			mux.pending.WithLabelValues(handler).Dec()
		}
	}
}

func (mux *ServeMux) requestsAfterFunc() afterFunc {
	switch {
	case mux.method && mux.code:
		return func(handler, method, code string) {
			mux.requests.WithLabelValues(handler, method, code).Inc()
		}
	case mux.method:
		return func(handler, method, code string) {
			mux.requests.WithLabelValues(handler, method).Inc()
		}
	case mux.code:
		return func(handler, method, code string) {
			mux.requests.WithLabelValues(handler, code).Inc()
		}
	default:
		return func(handler, method, code string) {
			mux.requests.WithLabelValues(handler).Inc()
		}
	}
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
