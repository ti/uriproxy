// Copyright 2017 Leenanxi
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package uriproxy

import (
	"testing"
	"time"
	"net/url"
	"net/http"
	"sync/atomic"
	"fmt"
	"net/http/httptest"
	"net"
)

func TestNewHost(t *testing.T) {
	upstream := &staticUpstream{
		FailTimeout: 10 * time.Second,
		MaxConns:    1,
		MaxFails:    1,
	}

	uh, err := upstream.NewHost(mustParseURL("http://example.com"))
	if err != nil {
		t.Error("Expected no error")
	}
	if uh.URL.String() != "http://example.com" {
		t.Error("Expected default schema to be added to Name.")
	}
	if uh.FailTimeout != upstream.FailTimeout {
		t.Error("Expected default FailTimeout to be set.")
	}
	if uh.MaxConns != upstream.MaxConns {
		t.Error("Expected default MaxConns to be set.")
	}
	if uh.CheckDown == nil {
		t.Error("Expected default CheckDown to be set.")
	}
	if uh.CheckDown(uh) {
		t.Error("Expected new host not to be down.")
	}
	// mark Unhealthy
	uh.Unhealthy = 1
	if !uh.CheckDown(uh) {
		t.Error("Expected unhealthy host to be down.")
	}
	// mark with Fails
	uh.Unhealthy = 0
	uh.Fails = 1
	if !uh.CheckDown(uh) {
		t.Error("Expected failed host to be down.")
	}
}

func TestHealthCheck(t *testing.T) {
	upstream := &staticUpstream{
		Hosts:       testPool(),
		Policy:      &Random{},
		FailTimeout: 10 * time.Second,
		MaxFails:    1,
	}
	upstream.healthCheck()
	if upstream.Hosts[0].Down() {
		t.Error("Expected first host in testpool to not fail healthcheck.")
	}
	if !upstream.Hosts[1].Down() {
		t.Error("Expected second host in testpool to fail healthcheck.")
	}
}

func TestSelect(t *testing.T) {
	upstream := &staticUpstream{
		Hosts:       testPool()[:3],
		Policy:      &Random{},
		FailTimeout: 10 * time.Second,
		MaxFails:    1,
	}
	r, _ := http.NewRequest("GET", "/", nil)
	upstream.Hosts[0].Unhealthy = 1
	upstream.Hosts[1].Unhealthy = 1
	upstream.Hosts[2].Unhealthy = 1
	if h := upstream.Select(r); h != nil {
		t.Error("Expected select to return nil as all host are down")
	}
	upstream.Hosts[2].Unhealthy = 0
	if h := upstream.Select(r); h == nil {
		t.Error("Expected select to not return nil")
	}
	upstream.Hosts[0].Conns = 1
	upstream.Hosts[0].MaxConns = 1
	upstream.Hosts[1].Conns = 1
	upstream.Hosts[1].MaxConns = 1
	upstream.Hosts[2].Conns = 1
	upstream.Hosts[2].MaxConns = 1
	if h := upstream.Select(r); h != nil {
		t.Error("Expected select to return nil as all hosts are full")
	}
	upstream.Hosts[2].Conns = 0
	if h := upstream.Select(r); h == nil {
		t.Error("Expected select to not return nil")
	}
}

func TestRegisterPolicy(t *testing.T) {
	name := "custom"
	customPolicy := &customPolicy{}
	RegisterPolicy(name, func(string) Policy { return customPolicy })
	if _, ok := supportedPolicies[name]; !ok {
		t.Error("Expected supportedPolicies to have a custom policy.")
	}

}

func TestAllowedPaths(t *testing.T) {
	upstream := &staticUpstream{
		IgnoredSubPaths: []string{"/download", "/static"},
	}
	tests := []struct {
		url      string
		expected bool
	}{
		{"/", true},
		{"/dl", true},
		{"/download", false},
		{"/download/static", false},
		{"/static", false},
		{"/static/download", false},
		{"/something/download", true},
		{"/something/static", true},
		{"//static", false},
		{"//static//download", false},
		{"//download", false},
	}

	for i, test := range tests {
		allowed := upstream.AllowedPath(test.url)
		if test.expected != allowed {
			t.Errorf("Test %d: expected %v found %v", i+1, test.expected, allowed)
		}
	}
}

func TestParseBlockHealthCheck(t *testing.T) {
	tests := []struct {
		config   url.Values
		interval string
		timeout  string
	}{
		{
			url.Values{"health_check": {"/health"}, "health_check_interval": {"10s"}, "health_check_timeout": {"20s"},},
			"10s",
			"20s",
		},
		{
			url.Values{"health_check": {"/health"}},
			"30s",
			"1m0s",
		}, {
			url.Values{"health_check": {"/health"}, "health_check_interval": {"15s"},},
			"15s",
			"1m0s",
		}, {
			url.Values{"health_check": {"/health"}, "health_check_interval": {"10s"}, "health_check_timeout": {"20s"},},
			"10s",
			"20s",
		}, {
			url.Values{"health_check": {"/health"}, "health_check_time": {"15s"},},
			"30s",
			"1m0s",
		},
	}

	for i, test := range tests {
		u := staticUpstream{}
		u.parseFromQuery(test.config)
		if u.HealthCheck.Interval.String() != test.interval {
			t.Errorf(
				"Test %d: HealthCheck interval not the same from config. Got %v. Expected: %v",
				i+1,
				u.HealthCheck.Interval,
				test.interval,
			)
		}
		if u.HealthCheck.Timeout.String() != test.timeout {
			t.Errorf(
				"Test %d: HealthCheck timeout not the same from config. Got %v. Expected: %v",
				i+1,
				u.HealthCheck.Timeout,
				test.timeout,
			)
		}
	}
}

func TestStop(t *testing.T) {
	config := "%s?health_check=/healthcheck&health_check_interval=%dms"
	tests := []struct {
		name                    string
		intervalInMilliseconds  int
		numHealthcheckIntervals int
	}{
		{
			"No Healthchecks After Stop - 5ms, 1 intervals",
			5,
			1,
		},
		{
			"No Healthchecks After Stop - 5ms, 2 intervals",
			5,
			2,
		},
		{
			"No Healthchecks After Stop - 5ms, 3 intervals",
			5,
			3,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {

			// Set up proxy.
			var counter int64
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r.Body.Close()
				atomic.AddInt64(&counter, 1)
			}))

			defer backend.Close()
			proxyUri := fmt.Sprintf(config, backend.URL, test.intervalInMilliseconds)
			upstream, err := NewStaticUpstream(proxyUri)
			if err != nil {
				t.Error("Expected no error. Got:", err.Error())
			}

			// Give some time for healthchecks to hit the server.
			time.Sleep(time.Duration(test.intervalInMilliseconds*test.numHealthcheckIntervals) * time.Millisecond)

			if err := upstream.Stop(); err != nil {
				t.Error("Expected no error stopping upstream. Got: ", err.Error())
			}

			counterValueAfterShutdown := atomic.LoadInt64(&counter)

			// Give some time to see if healthchecks are still hitting the server.
			time.Sleep(time.Duration(test.intervalInMilliseconds*test.numHealthcheckIntervals) * time.Millisecond)

			if counterValueAfterShutdown == 0 {
				t.Error("Expected healthchecks to hit test server. Got no healthchecks.")
			}

			counterValueAfterWaiting := atomic.LoadInt64(&counter)
			if counterValueAfterWaiting != counterValueAfterShutdown {
				t.Errorf("Expected no more healthchecks after shutdown. Got: %d healthchecks after shutdown", counterValueAfterWaiting-counterValueAfterShutdown)
			}

		})

	}
}

func TestParseQuery(t *testing.T) {
	r, _ := http.NewRequest("GET", "/", nil)
	tests := []struct {
		config string
	}{
		// Test #1: transparent preset
		{"http://localhost:8080?transparent=true"},

		// Test #2: transparent preset with another param
		{"http://localhost:8080?transparent&header_upstream[X-Test]=Tester"},
	}

	for i, test := range tests {
		upstream, err := NewStaticUpstream(test.config)
		if err != nil {
			t.Errorf("Expected no error. Got: %s", err.Error())
		}
		headers := upstream.Select(r).UpstreamHeaders

		if _, ok := headers["Host"]; !ok {
			t.Errorf("Test %d: Could not find the Host header", i+1)
		}

		if _, ok := headers["X-Real-Ip"]; !ok {
			t.Errorf("Test %d: Could not find the X-Real-Ip header", i+1)
		}

		if _, ok := headers["X-Forwarded-Proto"]; !ok {
			t.Errorf("Test %d: Could not find the X-Forwarded-Proto header", i+1)
		}
	}
}

func TestHealthSetUp(t *testing.T) {
	// tests for insecure skip verify
	tests := []struct {
		config string
		flag   bool
	}{
		// Test #1: without flag
		{"http://localhost:8080?health_check=/", false},

		// Test #2: with flag
		{"http://localhost:8080?health_check=/&insecure_skip_verify", true},
	}

	for i, test := range tests {
		upstream, err := NewStaticUpstream(test.config)
		if err != nil {
			t.Errorf("Expected no error. Got: %s", err.Error())
		}
		staticUpstream, ok := upstream.(*staticUpstream)
		if !ok {
			t.Errorf("Type mismatch: %#v", upstream)
			continue
		}
		transport, ok := staticUpstream.HealthCheck.Client.Transport.(*http.Transport)
		if !ok {
			t.Errorf("Type mismatch: %#v", staticUpstream.HealthCheck.Client.Transport)
			continue
		}
		if test.flag != transport.TLSClientConfig.InsecureSkipVerify {
			t.Errorf("Test %d: expected transport.TLSClientCnfig.InsecureSkipVerify=%v, got %v", i, test.flag, transport.TLSClientConfig.InsecureSkipVerify)
		}
	}
}

func TestHealthCheckHost(t *testing.T) {
	// tests for upstream host on health checks
	tests := []struct {
		config string
		flag   bool
		host   string
	}{
		// Test #1: without upstream header
		{"http://localhost:8080?health_check=/&host=example.com", false, "example.com"},

		// Test #2: without upstream header, missing host
		{"http://localhost:8080?health_check=/", true, ""},

		// Test #3: with upstream header (via transparent preset)
		{ "http://localhost:8080?health_check=/&transparent&host=foo.example.com", true, "foo.example.com"},

		// Test #4: with upstream header (explicit header)
		{"http://localhost:8080?health_check=/&header_upstream[Host]={host}&host=example.com", true, "example.com"},

		// Test #5: with upstream header, missing host
		{"http://localhost:8080?health_check=/&transparent", true, ""},
	}

	for i, test := range tests {
		upstream, err := NewStaticUpstream(test.config)
		if err != nil {
			t.Errorf("Expected no error. Got: %s", err.Error())
		}
		staticUpstream, ok := upstream.(*staticUpstream)
		if !ok {
			t.Errorf("Type mismatch: %#v", upstream)
			continue
		}
		if test.flag != (staticUpstream.HealthCheck.Host == test.host) {
			t.Errorf("Test %d: expected staticUpstream.HealthCheck.Host=%v, got %v", i, test.host, staticUpstream.HealthCheck.Host)
		}
	}
}

func TestHealthCheckPort(t *testing.T) {
	var counter int64

	healthCounter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body.Close()
		atomic.AddInt64(&counter, 1)
	}))

	_, healthPort, err := net.SplitHostPort(healthCounter.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	defer healthCounter.Close()

	tests := []struct {
		config string
	}{
		// Test #1: upstream with port
		{"http://localhost:8080?health_check=/&health_check_port=" + healthPort},

		// Test #2: upstream without port (default to 80)
		{"http://localhost?health_check=/&health_check_port=" + healthPort},
	}

	for i, test := range tests {
		counterValueAtStart := atomic.LoadInt64(&counter)
		upstream, err := NewStaticUpstream(test.config)
		if err != nil {
			t.Error("Expected no error. Got:", err.Error())
		}

		// Give some time for healthchecks to hit the server.
		time.Sleep(500 * time.Millisecond)

		if err := upstream.Stop(); err != nil {
			t.Errorf("Test %d: Expected no error stopping upstream. Got: %v", i, err.Error())
		}

		counterValueAfterShutdown := atomic.LoadInt64(&counter)

		if counterValueAfterShutdown == counterValueAtStart {
			t.Errorf("Test %d: Expected healthchecks to hit test server. Got no healthchecks.", i)
		}
	}

	t.Run("valid_port", func(t *testing.T) {
		tests := []struct {
			config string
		}{
			// Test #1: invalid port (nil)
			{ "http://localhost?health_check=/&health_check_port"},

			// Test #2: invalid port (string)
			{"http://localhost?health_check=/&health_check_port=abc"},

			// Test #3: invalid port (negative)
			{"http://localhost?health_check=/&health_check_port=-1"},
		}

		for i, test := range tests {
			_, err := NewStaticUpstream(test.config)
			if err == nil {
				t.Errorf("Test %d accepted invalid config", i)
			}
		}
	})

}

func TestHealthCheckContentString(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "blablabla good blablabla")
		r.Body.Close()
	}))
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	tests := []struct {
		config        string
		shouldContain bool
	}{
		{"http://localhost:" + port + "?health_check=/testhealth&health_check_contains=good",
			true,
		},
		{"http://localhost:" + port + "?health_check=/testhealth&health_check_port=" + port + "&health_check_contains=bad",
			false,
		},
	}
	for i, test := range tests {
		upstream, err := NewStaticUpstream(test.config)
		if err != nil {
			t.Errorf("Expected no error. Test %d Got: %s", i, err.Error())
		}
		staticUpstream, ok := upstream.(*staticUpstream)
		if !ok {
			t.Errorf("Type mismatch: %#v", upstream)
			continue
		}
		staticUpstream.healthCheck()
		for _, host := range staticUpstream.Hosts {
			if test.shouldContain && atomic.LoadInt32(&host.Unhealthy) == 0 {
				// healthcheck url was hit and the required test string was found
				continue
			}
			if !test.shouldContain && atomic.LoadInt32(&host.Unhealthy) != 0 {
				// healthcheck url was hit and the required string was not found
				continue
			}
			t.Errorf("Health check bad response")
		}
		upstream.Stop()
	}
}

