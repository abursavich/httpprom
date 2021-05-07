package httpprom

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestServerMux(t *testing.T) {
	tests := []struct {
		name    string
		muxOpts []ServeMuxOption
		hndOpts []HandlerOption
		expect  string
	}{
		{
			name: "Default",
			expect: `
				# HELP http_server_requests_pending Number of HTTP server requests currently pending.
				# TYPE http_server_requests_pending gauge
				http_server_requests_pending{handler="/"} 1
				# HELP http_server_requests_total Total number of HTTP server requests completed.
				# TYPE http_server_requests_total gauge
				http_server_requests_total{handler="/"} 3
			`,
		},
		{
			name:    "WithCode",
			muxOpts: []ServeMuxOption{WithCode()},
			expect: `
				# HELP http_server_requests_pending Number of HTTP server requests currently pending.
				# TYPE http_server_requests_pending gauge
				http_server_requests_pending{handler="/"} 1
				# HELP http_server_requests_total Total number of HTTP server requests completed.
				# TYPE http_server_requests_total gauge
				http_server_requests_total{code="200",handler="/"} 3
			`,
		},
		{
			name:    "WithMethod",
			muxOpts: []ServeMuxOption{WithMethod()},
			expect: `
				# HELP http_server_requests_pending Number of HTTP server requests currently pending.
				# TYPE http_server_requests_pending gauge
				http_server_requests_pending{handler="/",method="get"} 1
				# HELP http_server_requests_total Total number of HTTP server requests completed.
				# TYPE http_server_requests_total gauge
				http_server_requests_total{handler="/",method="get"} 3
			`,
		},
		{
			name:    "WithConstLabels",
			muxOpts: []ServeMuxOption{WithConstLabels(prometheus.Labels{"foo": "bar"})},
			expect: `
				# HELP http_server_requests_pending Number of HTTP server requests currently pending.
				# TYPE http_server_requests_pending gauge
				http_server_requests_pending{foo="bar",handler="/"} 1
				# HELP http_server_requests_total Total number of HTTP server requests completed.
				# TYPE http_server_requests_total gauge
				http_server_requests_total{foo="bar",handler="/"} 3
			`,
		},
		{
			name:    "WithCodeAndMethod",
			muxOpts: []ServeMuxOption{WithCode(), WithMethod()},
			expect: `
				# HELP http_server_requests_pending Number of HTTP server requests currently pending.
				# TYPE http_server_requests_pending gauge
				http_server_requests_pending{handler="/",method="get"} 1
				# HELP http_server_requests_total Total number of HTTP server requests completed.
				# TYPE http_server_requests_total gauge
				http_server_requests_total{code="200",handler="/",method="get"} 3
			`,
		},
		{
			name:    "WithName",
			hndOpts: []HandlerOption{WithName("test")},
			expect: `
				# HELP http_server_requests_pending Number of HTTP server requests currently pending.
				# TYPE http_server_requests_pending gauge
				http_server_requests_pending{handler="test"} 1
				# HELP http_server_requests_total Total number of HTTP server requests completed.
				# TYPE http_server_requests_total gauge
				http_server_requests_total{handler="test"} 3
			`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := make(chan bool)
			mux := NewServeMux(tt.muxOpts...)
			mux.HandleFunc("/",
				func(w http.ResponseWriter, r *http.Request) {
					<-ch
					<-ch
				},
				tt.hndOpts...,
			)
			srv := httptest.NewServer(mux)
			defer srv.Close()
			defer close(ch) // NB: srv.Close() blocks until all requests are complete
			doReq := func() {
				req, err := http.NewRequest("GET", srv.URL, nil)
				check(t, err)
				resp, err := srv.Client().Do(req)
				check(t, err)
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
			for i := 0; i < 3; i++ {
				go func() {
					ch <- true // pending
					ch <- true // released
				}()
				doReq()
			}
			go doReq()
			ch <- true // pending
			check(t, testutil.CollectAndCompare(mux.Collector(), strings.NewReader(tt.expect)))
		})
	}
}

func check(t *testing.T, err error) {
	if err != nil {
		t.Helper()
		t.Fatal(err)
	}
}

func TestCoalesce(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		out  []string
	}{
		{
			name: "empty",
			in:   []string{},
			out:  []string{},
		},
		{
			name: "head",
			in:   []string{"", "a", "b", "c"},
			out:  []string{"a", "b", "c"},
		},
		{
			name: "middle",
			in:   []string{"a", "b", "", "c", "d"},
			out:  []string{"a", "b", "c", "d"},
		},
		{
			name: "tail",
			in:   []string{"a", "b", "c", ""},
			out:  []string{"a", "b", "c"},
		},
		{
			name: "many",
			in:   []string{"", "", "a", "", "", "", "b", "", "c", "d", "", ""},
			out:  []string{"a", "b", "c", "d"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if diff := cmp.Diff(coalesce(tt.in...), tt.out); diff != "" {
				t.Errorf("unexpected diff:\n%s", diff)
			}
		})
	}
}
