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
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"crypto/tls"
	"errors"
	"context"
)

var (
	supportedPolicies = make(map[string]func(string) Policy)
)

//ServeUpstream  Server Http ResponseWriter and Request to Upsteam
func ServeUpstream(upstream Upstream, w http.ResponseWriter, r *http.Request, respRespUpdateFn RespUpdateFn) (int, error) {
	// this replacer is used to fill in header field values
	replacer := newReplacer(r, "")

	// outreq is the request that makes a roundtrip to the backend
	outreq, cancel := createUpstreamRequest(w, r)
	defer cancel()

	// If we have more than one upstream host defined and if retrying is enabled
	// by setting try_duration to a non-zero value, caddy will try to
	// retry the request at a different host if the first one failed.
	//
	// This requires us to possibly rewind and replay the request body though,
	// which in turn requires us to buffer the request body first.
	//
	// An unbuffered request is usually preferrable, because it reduces latency
	// as well as memory usage. Furthermore it enables different kinds of
	// HTTP streaming applications like gRPC for instance.
	requiresBuffering := upstream.GetHostCount() > 1 && upstream.GetTryDuration() != 0

	if requiresBuffering {
		body, err := newBufferedBody(outreq.Body)
		if err != nil {
			return http.StatusBadRequest, errors.New("failed to read downstream request body")
		}
		if body != nil {
			outreq.Body = body
		}
	}

	// The keepRetrying function will return true if we should
	// loop and try to select another host, or false if we
	// should break and stop retrying.
	start := time.Now()

	keepRetrying := func(backendErr error) bool {
		// if downstream has canceled the request, break
		if backendErr == context.Canceled {
			return false
		}
		// if we've tried long enough, break
		if time.Since(start) >= upstream.GetTryDuration() {
			return false
		}
		// otherwise, wait and try the next available host
		time.Sleep(upstream.GetTryInterval())
		return true
	}

	var backendErr error
	for {
		// since Select() should give us "up" hosts, keep retrying
		// hosts until timeout (or until we get a nil host).
		host := upstream.Select(r)
		if host == nil {
			if backendErr == nil {
				backendErr = ErrNoAvailableUpstream
				//TODO: Consider to remove it ?
				break
			}
			if !keepRetrying(backendErr) {
				break
			}
			continue
		}

		proxy := host.ReverseProxy

		nameURL := host.URL

		// a backend's name may contain more than just the host,
		// so we parse it as a URL to try to isolate the host.
		outreq.Host = nameURL.Host
		if proxy == nil {
			proxy = NewSingleHostReverseProxy(&nameURL, host.WithoutPathPrefix, http.DefaultMaxIdleConnsPerHost)
		}
		// use upstream credentials by default
		if nameURL.User != nil {
			pwd, _ := nameURL.User.Password()
			outreq.SetBasicAuth(nameURL.User.Username(), pwd)
		}

		if proxy == nil {
			return http.StatusInternalServerError, errors.New("proxy for host '" + host.URL.String() + "' is nil")
		}

		// set headers for request going upstream
		if host.UpstreamHeaders != nil {
			// modify headers for request that will be sent to the upstream host
			mutateHeadersByRules(outreq.Header, host.UpstreamHeaders, replacer)
			if hostHeaders, ok := outreq.Header["Host"]; ok && len(hostHeaders) > 0 {
				outreq.Host = hostHeaders[len(hostHeaders)-1]
			}
		}

		// Before we retry the request we have to make sure
		// that the body is rewound to it's beginning.
		if bb, ok := outreq.Body.(*bufferedBody); ok {
			if err := bb.rewind(); err != nil {
				return http.StatusInternalServerError, errors.New("unable to rewind downstream request body")
			}
		}


		var respUpdateFns []RespUpdateFn

		// prepare a function that will update response
		// headers coming back downstream
		if host.DownstreamHeaders != nil && len(host.DownstreamHeaders) > 0 {
			downHeaderUpdateFn := createRespHeaderUpdateFn(host.DownstreamHeaders, replacer)
			respUpdateFns = append(respUpdateFns, downHeaderUpdateFn)
		}
		if respRespUpdateFn  != nil {
			respUpdateFns = append(respUpdateFns, respRespUpdateFn)
		}

		// tell the proxy to serve the request
		//
		// NOTE:
		//   The call to proxy.ServeHTTP can theoretically panic.
		//   To prevent host.Conns from getting out-of-sync we thus have to
		//   make sure that it's _always_ correctly decremented afterwards.
		func() {
			atomic.AddInt64(&host.Conns, 1)
			defer atomic.AddInt64(&host.Conns, -1)
			backendErr = proxy.ServeHTTP(w, outreq, respUpdateFns)
		}()

		// if no errors, we're done here
		if backendErr == nil {
			return 0, nil
		}

		if backendErr == ErrMaxBytesExceeded {
			return http.StatusRequestEntityTooLarge, backendErr
		}

		// failover; remember this failure for some time if
		// request failure counting is enabled
		timeout := host.FailTimeout
		if timeout > 0 {
			atomic.AddInt32(&host.Fails, 1)
			go func(host *UpstreamHost, timeout time.Duration) {
				time.Sleep(timeout)
				atomic.AddInt32(&host.Fails, -1)
			}(host, timeout)
		}

		retry := keepRetrying(backendErr)

		// if we've tried long enough, break
		if !retry {
			break
		}
	}
	return http.StatusBadGateway, backendErr
}


//DoUpstream get upstream response
func DoUpstream(upstream Upstream, req *http.Request) (resp *http.Response, backendErrerr error){
	// The keepRetrying function will return true if we should
	// loop and try to select another host, or false if we
	// should break and stop retrying.
	start := time.Now()

	keepRetrying := func(backendErr error) bool {
		// if downstream has canceled the request, break
		if backendErr == context.Canceled {
			return false
		}
		// if we've tried long enough, break
		if time.Since(start) >= upstream.GetTryDuration() {
			return false
		}
		if !strings.Contains(backendErr.Error(), "connection refused") {
			return false
		}
		// otherwise, wait and try the next available host
		time.Sleep(upstream.GetTryInterval())
		return true
	}

	var backendErr error
	for {
		// since Select() should give us "up" hosts, keep retrying
		// hosts until timeout (or until we get a nil host).
		host := upstream.Select(req)

		if host == nil {
			if backendErr == nil {
				backendErr = ErrNoAvailableUpstream

				return nil, ErrNoAvailableUpstream
				break
			}
			if !keepRetrying(backendErr) {
				break
			}
			continue
		}
		req.Host = host.URL.Host
		req.URL.Host = host.URL.Host
		resp, backendErr = http.DefaultClient.Do(req)

		if backendErr == nil {
			return resp, backendErr
		}

		if backendErr == ErrMaxBytesExceeded {
			return nil, ErrMaxBytesExceeded
		}

		// failover; remember this failure for some time if
		// request failure counting is enabled
		timeout := host.FailTimeout
		if timeout > 0 {
			atomic.AddInt32(&host.Fails, 1)
			go func(host *UpstreamHost, timeout time.Duration) {
				time.Sleep(timeout)
				atomic.AddInt32(&host.Fails, -1)
			}(host, timeout)
		}
		retry := keepRetrying(backendErr)
		// if we've tried long enough, break
		if !retry {
			break
		}
	}
	if backendErr == nil {
		backendErr = errors.New(http.StatusText(http.StatusBadGateway))
	}
	return nil,backendErr
}

// Upstream manages a pool of proxy upstream hosts.
type Upstream interface {
	// Selects an upstream host to be routed to. It
	// should return a suitable upstream host, or nil
	// if no such hosts are available.
	Select(*http.Request) *UpstreamHost

	// Checks if subpath is not an ignored path
	AllowedPath(string) bool

	// Gets how long to try selecting upstream hosts
	// in the case of cascading failures.
	GetTryDuration() time.Duration

	// Gets how long to wait between selecting upstream
	// hosts in the case of cascading failures.
	GetTryInterval() time.Duration

	// Gets the number of upstream hosts.
	GetHostCount() int

	// Stops the upstream from proxying requests to shutdown goroutines cleanly.
	Stop() error
}

// NewStaticUpstream parses the configuration input and sets up
// static upstream for the proxy middleware
func NewStaticUpstream(rawurl string) (Upstream, error) {
	uri, err := url.Parse(rawurl)
	if err != nil {
		return nil, err
	}
	return newStaticUpstreamByURL(uri)
}

// NewStaticUpstream parses the configuration input and sets up
// static upstream for the proxy middleware
func newStaticUpstreamByURL(uri *url.URL) (Upstream, error) {
	upstream := &staticUpstream{
		stop:              make(chan struct{}),
		upstreamHeaders:   make(http.Header),
		downstreamHeaders: make(http.Header),
		Hosts:             nil,
		Policy:            &RoundRobin{},
		MaxFails:          1,
		FailTimeout:       30 * time.Second,
		TryDuration:       5 * time.Second,
		TryInterval:       250 * time.Millisecond,
		MaxConns:          0,
		KeepAlive:         http.DefaultMaxIdleConnsPerHost,
	}
	query := uri.Query()
	if err := upstream.parseFromQuery(query); err != nil {
		return nil, err
	}
	to  := strings.Split(uri.Host, ",")
	if len(to) == 0 {
		return nil, errors.New("upstream uri hosts must greater than 1")
	}
	upstream.Hosts = make([]*UpstreamHost, len(to))
	for i, host := range to {
		tmp := *uri
		tmp.Host = host
		tmp.RawQuery = ""
		tmp.Fragment = ""
		uh, err := upstream.NewHost(tmp)
		if err != nil {
			return nil, err
		}
		upstream.Hosts[i] = uh
	}
	if upstream.HealthCheck.Path != "" {
		upstream.HealthCheck.Client = http.Client{
			Timeout: upstream.HealthCheck.Timeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: upstream.insecureSkipVerify},
			},
		}
		host := query.Get("host")
		// set up health check upstream host if we have one
		if host != "" {
			hostHeader := upstream.upstreamHeaders.Get("Host")
			if strings.Contains(hostHeader, "{host}") {
				upstream.HealthCheck.Host = strings.Replace(hostHeader, "{host}", host, -1)
			}
		}
		upstream.wg.Add(1)
		go func() {
			defer upstream.wg.Done()
			upstream.HealthCheckWorker(upstream.stop)
		}()
	}
	return upstream, nil
}


// RegisterPolicy adds a custom policy to the proxy.
func RegisterPolicy(name string, policy func(string) Policy) {
	supportedPolicies[name] = policy
}

type staticUpstream struct {
	upstreamHeaders   http.Header
	downstreamHeaders http.Header
	stop              chan struct{}  // Signals running goroutines to stop.
	wg                sync.WaitGroup // Used to wait for running goroutines to stop.
	Hosts             HostPool
	Policy            Policy
	KeepAlive         int
	FailTimeout       time.Duration
	TryDuration       time.Duration
	TryInterval       time.Duration
	MaxConns          int64
	HealthCheck struct {
		Client        http.Client
		Path          string
		Interval      time.Duration
		Timeout       time.Duration
		Host          string
		Port          string
		ContentString string
	}
	WithoutPathPrefix  string
	IgnoredSubPaths    []string
	insecureSkipVerify bool
	MaxFails           int32
}


func (u *staticUpstream) NewHost(URL url.URL) (*UpstreamHost, error) {
	if URL.Scheme == "" {
		URL.Scheme = "http"
	}
	uh := &UpstreamHost{
		URL:              URL,
		Conns:             0,
		Fails:             0,
		FailTimeout:       u.FailTimeout,
		Unhealthy:         0,
		UpstreamHeaders:   u.upstreamHeaders,
		DownstreamHeaders: u.downstreamHeaders,
		CheckDown: func(u *staticUpstream) UpstreamHostDownFunc {
			return func(uh *UpstreamHost) bool {
				if atomic.LoadInt32(&uh.Unhealthy) != 0 {
					return true
				}
				if atomic.LoadInt32(&uh.Fails) >= u.MaxFails {
					return true
				}
				return false
			}
		}(u),
		WithoutPathPrefix: u.WithoutPathPrefix,
		MaxConns:          u.MaxConns,
	}
	uh.ReverseProxy = NewSingleHostReverseProxy(&uh.URL, uh.WithoutPathPrefix, u.KeepAlive)
	if u.insecureSkipVerify {
		uh.ReverseProxy.UseInsecureTransport()
	}

	return uh, nil
}

func (u *staticUpstream) healthCheck() {
	for _, host := range u.Hosts {
		hostURL :=  host.URL
		if u.HealthCheck.Port != "" {
			replacePort(&hostURL, u.HealthCheck.Port)
		}
		hostURL.Path = u.HealthCheck.Path


		unhealthy := func() bool {
			// set up request, needed to be able to modify headers
			// possible errors are bad HTTP methods or un-parsable urls
			req, err := http.NewRequest("GET", hostURL.String(), nil)
			if err != nil {
				return true
			}
			// set host for request going upstream
			if u.HealthCheck.Host != "" {
				req.Host = u.HealthCheck.Host
			}
			r, err := u.HealthCheck.Client.Do(req)
			if err != nil {
				return true
			}
			defer func() {
				io.Copy(ioutil.Discard, r.Body)
				r.Body.Close()
			}()
			if r.StatusCode < 200 || r.StatusCode >= 400 {
				return true
			}
			if u.HealthCheck.ContentString == "" { // don't check for content string
				return false
			}
			// TODO ReadAll will be replaced if deemed necessary
			//      See https://github.com/mholt/caddy/pull/1691
			buf, err := ioutil.ReadAll(r.Body)
			if err != nil {
				return true
			}
			if bytes.Contains(buf, []byte(u.HealthCheck.ContentString)) {
				return false
			}
			return true
		}()
		if unhealthy {
			atomic.StoreInt32(&host.Unhealthy, 1)
		} else {
			atomic.StoreInt32(&host.Unhealthy, 0)
		}
	}
}

func (u *staticUpstream) HealthCheckWorker(stop chan struct{}) {
	ticker := time.NewTicker(u.HealthCheck.Interval)
	u.healthCheck()
	for {
		select {
		case <-ticker.C:
			u.healthCheck()
		case <-stop:
			ticker.Stop()
			return
		}
	}
}

func (u *staticUpstream) Select(r *http.Request) *UpstreamHost {
	pool := u.Hosts
	if len(pool) == 1 {
		if !pool[0].Available() {
			return nil
		}
		return pool[0]
	}
	allUnavailable := true
	for _, host := range pool {
		if host.Available() {
			allUnavailable = false
			break
		}
	}
	if allUnavailable {
		return nil
	}
	if u.Policy == nil {
		return (&Random{}).Select(pool, r)
	}
	return u.Policy.Select(pool, r)
}

func (u *staticUpstream) AllowedPath(requestPath string) bool {
	for _, ignoredSubPath := range u.IgnoredSubPaths {
		if IsPathMatchBasePath(requestPath,ignoredSubPath) {
			return  false
		}
	}
	return true
}

// GetTryDuration returns u.TryDuration.
func (u *staticUpstream) GetTryDuration() time.Duration {
	return u.TryDuration
}

// GetTryInterval returns u.TryInterval.
func (u *staticUpstream) GetTryInterval() time.Duration {
	return u.TryInterval
}


func (u *staticUpstream) GetHostCount() int {
	return len(u.Hosts)
}

// Stop sends a signal to all goroutines started by this staticUpstream to exit
// and waits for them to finish before returning.
func (u *staticUpstream) Stop() error {
	close(u.stop)
	u.wg.Wait()
	return nil
}
//parseFromQuery parse Upstream params from url
func (u *staticUpstream) parseFromQuery(q url.Values) error {
	for k, _ := range q {
		index := strings.Index(k, "[")
		v := q.Get(k)
		if index > 0 {
			l := len(k)
			if l - index > 2 {
				childKey := k[index+1:l-1]
				switch k[:index] {
				case "header_upstream":
					if v == "" {
						if !strings.HasPrefix(childKey, "-") {
							return errors.New("header_upstream must has prefix '-'")
						}
					}
					u.upstreamHeaders.Add(childKey, v)
				case "header_downstream":
					if v == "" {
						if !strings.HasPrefix(childKey, "-") {
							return errors.New("header_upstream must has prefix '-'")
						}
					}
					u.downstreamHeaders.Add(childKey, v)
				}
			} else {
				return errors.New("query key bad format for [")
			}
			continue
		}
		switch k {
		case "host":
			//Do Nothing
		case "policy":
			policyCreateFunc, ok := supportedPolicies[v]
			if !ok {
				return errors.New("policy must be in (random, least_conn, round_robin, ip_hash, first, uri_hash, header)")
			}
			u.Policy = policyCreateFunc(v)
		case "fail_timeout":
			dur, err := time.ParseDuration(v)
			if err != nil {
				return err
			}
			u.FailTimeout = dur
		case "max_fails":
			n, err := strconv.Atoi(v)
			if err != nil {
				return err
			}
			if n < 1 {
				return errors.New("max_fails must be at least 1")
			}
			u.MaxFails = int32(n)
		case "try_duration":
			dur, err := time.ParseDuration(v)
			if err != nil {
				return err
			}
			u.TryDuration = dur
		case "try_interval":
			interval, err := time.ParseDuration(v)
			if err != nil {
				return err
			}
			u.TryInterval = interval
		case "max_conns":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return err
			}
			u.MaxConns = n
		case "health_check":
			u.HealthCheck.Path = v
			// Set defaults
			if u.HealthCheck.Interval == 0 {
				u.HealthCheck.Interval = 30 * time.Second
			}
			if u.HealthCheck.Timeout == 0 {
				u.HealthCheck.Timeout = 60 * time.Second
			}
		case "health_check_interval":
			dur, err := time.ParseDuration(v)
			if err != nil {
				return err
			}
			u.HealthCheck.Interval = dur
		case "health_check_timeout":
			dur, err := time.ParseDuration(v)
			if err != nil {
				return err
			}
			u.HealthCheck.Timeout = dur
		case "health_check_port":
			n, err := strconv.Atoi(v)
			if err != nil {
				return err
			}
			if n < 0 {
				return fmt.Errorf("invalid health_check_port '%s'", v)
			}
			u.HealthCheck.Port = v
		case "health_check_contains":
			u.HealthCheck.ContentString = v
		case "header_upstream":
			return errors.New("please use header_upstream[-Key]=Value format")
		case "header_downstream":
			return errors.New("please use header_downstream[-Key]=Value format")
		case "transparent":
			u.upstreamHeaders.Add("Host", "{host}")
			u.upstreamHeaders.Add("X-Real-IP", "{remote}")
			u.upstreamHeaders.Add("X-Forwarded-For", "{remote}")
			u.upstreamHeaders.Add("X-Forwarded-Proto", "{scheme}")
		case "websocket":
			u.upstreamHeaders.Add("Connection", "{>Connection}")
			u.upstreamHeaders.Add("Upgrade", "{>Upgrade}")
		case "without":
			u.WithoutPathPrefix = v
		case "except":
			ignoredPaths := strings.Split(v, ",")
			if len(ignoredPaths) == 0 {
				return errors.New("except can not be empty")
			}
			u.IgnoredSubPaths = ignoredPaths
		case "insecure_skip_verify":
			u.insecureSkipVerify = true
		case "keepalive":
			n, err := strconv.Atoi(v)
			if err != nil {
				return err
			}
			u.KeepAlive = n
		default:
			return fmt.Errorf("unknown argument '%s'", k)
		}
	}
	return nil
}
// UpstreamHostDownFunc can be used to customize how Down behaves.
type UpstreamHostDownFunc func(*UpstreamHost) bool

// UpstreamHost represents a single proxy upstream
type UpstreamHost struct {
	// This field is read & written to concurrently, so all access must use
	// atomic operations.
	Conns             int64 // must be first field to be 64-bit aligned on 32-bit systems
	MaxConns          int64
	URL               url.URL
	UpstreamHeaders   http.Header
	DownstreamHeaders http.Header
	FailTimeout       time.Duration
	CheckDown         UpstreamHostDownFunc
	WithoutPathPrefix string
	ReverseProxy      *ReverseProxy
	Fails             int32
	// This is an int32 so that we can use atomic operations to do concurrent
	// reads & writes to this value.  The default value of 0 indicates that it
	// is healthy and any non-zero value indicates unhealthy.
	Unhealthy int32
}

// Down checks whether the upstream host is down or not.
// Down will try to use uh.CheckDown first, and will fall
// back to some default criteria if necessary.
func (uh *UpstreamHost) Down() bool {
	if uh.CheckDown == nil {
		// Default settings
		return atomic.LoadInt32(&uh.Unhealthy) != 0 || atomic.LoadInt32(&uh.Fails) > 0
	}
	return uh.CheckDown(uh)
}

// Full checks whether the upstream host has reached its maximum connections
func (uh *UpstreamHost) Full() bool {
	return uh.MaxConns > 0 && atomic.LoadInt64(&uh.Conns) >= uh.MaxConns
}

// Available checks whether the upstream host is available for proxying to
func (uh *UpstreamHost) Available() bool {
	return !uh.Down() && !uh.Full()
}

func replacePort(parsedURL *url.URL, newPort string) {
	// handles 'localhost' and 'localhost:8080'
	parsedHost, _, err := net.SplitHostPort(parsedURL.Host)
	if err != nil {
		parsedHost = parsedURL.Host
	}
	parsedURL.Host = net.JoinHostPort(parsedHost, newPort)
}

// createUpstremRequest shallow-copies r into a new request
// that can be sent upstream.
//
// Derived from reverseproxy.go in the standard Go httputil package.
func createUpstreamRequest(rw http.ResponseWriter, r *http.Request) (*http.Request, context.CancelFunc) {
	// Original incoming server request may be canceled by the
	// user or by std lib(e.g. too many idle connections).
	ctx, cancel := context.WithCancel(r.Context())
	if cn, ok := rw.(http.CloseNotifier); ok {
		notifyChan := cn.CloseNotify()
		go func() {
			select {
			case <-notifyChan:
				cancel()
			case <-ctx.Done():
			}
		}()
	}

	outreq := r.WithContext(ctx) // includes shallow copies of maps, but okay

	// We should set body to nil explicitly if request body is empty.
	// For server requests the Request Body is always non-nil.
	if r.ContentLength == 0 {
		outreq.Body = nil
	}

	// We are modifying the same underlying map from req (shallow
	// copied above) so we only copy it if necessary.
	copiedHeaders := false

	// Remove hop-by-hop headers listed in the "Connection" header.
	// See RFC 2616, section 14.10.
	if c := outreq.Header.Get("Connection"); c != "" {
		for _, f := range strings.Split(c, ",") {
			if f = strings.TrimSpace(f); f != "" {
				if !copiedHeaders {
					outreq.Header = make(http.Header)
					CopyHeader(outreq.Header, r.Header)
					copiedHeaders = true
				}
				outreq.Header.Del(f)
			}
		}
	}

	// Remove hop-by-hop headers to the backend. Especially
	// important is "Connection" because we want a persistent
	// connection, regardless of what the client sent to us.
	for _, h := range hopHeaders {
		if outreq.Header.Get(h) != "" {
			if !copiedHeaders {
				outreq.Header = make(http.Header)
				CopyHeader(outreq.Header, r.Header)
				copiedHeaders = true
			}
			outreq.Header.Del(h)
		}
	}

	if clientIP, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		// If we aren't the first proxy, retain prior
		// X-Forwarded-For information as a comma+space
		// separated list and fold multiple headers into one.
		if prior, ok := outreq.Header["X-Forwarded-For"]; ok {
			clientIP = strings.Join(prior, ", ") + ", " + clientIP
		}
		outreq.Header.Set("X-Forwarded-For", clientIP)
	}

	return outreq, cancel
}

func createRespHeaderUpdateFn(rules http.Header,repl *replacer) RespUpdateFn {
	return func(resp *http.Response) bool {
		mutateHeadersByRules(resp.Header, rules, repl)
		return  false
	}
}

func mutateHeadersByRules(headers, rules http.Header, repl *replacer) {
	for ruleField, ruleValues := range rules {
		if strings.HasPrefix(ruleField, "+") {
			for _, ruleValue := range ruleValues {
				replacement := repl.Replace(ruleValue)
				if len(replacement) > 0 {
					headers.Add(strings.TrimPrefix(ruleField, "+"), replacement)
				}

			}
		} else if strings.HasPrefix(ruleField, "-") {
			headers.Del(strings.TrimPrefix(ruleField, "-"))
		} else if len(ruleValues) > 0 {
			replacement := repl.Replace(ruleValues[len(ruleValues)-1])
			if len(replacement) > 0 {
				headers.Set(ruleField, replacement)
			}
		}
	}
}
