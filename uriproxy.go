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

// Package proxy is middleware that proxies HTTP requests.
package uriproxy

import (
	"net/http"
	"errors"
	"io"
	"net/url"
	"encoding/base64"
)

// DefaultProxy is the default Client like nginx
var DefaultProxy = &Proxy{
	Routers:[]Router{},
}

// Proxy represents a middleware instance that can proxy requests.
type Proxy struct {
	Next     Handler
	Routers []Router
}

// AddUpstream add upstream by uri
func (p *Proxy) AddUpstream(pattern, method, uri string) error {
	for _,r := range p.Routers  {
		if r.Pattern == pattern {
			return errors.New("pattern already added")
		}
	}
	upstream, err := NewStaticUpstream(uri)
	if err != nil {
		return err
	}
	p.Routers = append(p.Routers, Router{
		Method:method,
		Pattern:pattern,
		Handler:upstream,
	})
	return nil
}
// ServeHTTP satisfies the httpserver.Handler interface.
func (p *Proxy) Match(r *http.Request) Upstream {
	var u Upstream
	var longestMatch int
	for _, router := range p.Routers {
		basePath := router.Pattern
		if !IsPathMatchBasePath(r.URL.Path,basePath) || !router.Handler.AllowedPath(r.URL.Path) {
			continue
		}
		if router.Method != "" && r.Method != router.Method {
			continue
		}
		if len(basePath) > longestMatch {
			longestMatch = len(basePath)
			u = router.Handler
		}
	}
	return u
}

type Router struct {
	Method  string
	Pattern string
	Handler Upstream
}

// ServeHTTP satisfies the httpserver.Handler interface.
func (p *Proxy) DoProxy(w http.ResponseWriter, r *http.Request, respRespUpdateFn RespUpdateFn) (int, error) {
	// start by selecting most specific matching upstream config
	upstream := p.Match(r)
	if upstream == nil {
		return http.StatusNotFound, ErrRouterNotFound
	}
	return ServeUpstream(upstream, w,r, respRespUpdateFn)
}

// ServeHTTP satisfies the httpserver.Handler interface.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	code, err := p.DoProxy(w,r,nil)
	if err != nil {
		if code >= 400 {
			w.WriteHeader(code)
		} else {
			w.WriteHeader(502)
		}
		w.Write([]byte(err.Error()))
	}
}

// ServeHTTP satisfies the httpserver.Handler interface.
func (p *Proxy) Do(r *http.Request) (*http.Response, error){
	upstream := p.Match(r)
	return DoUpstream(upstream,r)
}


//Request is request by proxy
func (p *Proxy) Request(method, uri string, headers map[string]string, body io.Reader) (*http.Response, error){
	u, err := url.Parse(uri)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(method, uri, body)
	if err != nil {
		return  nil, err
	}
	if headers["Authorization"] == "" && u.User != nil {
		req.Header.Set("Authorization",base64.StdEncoding.EncodeToString([]byte(u.User.String())))
	}
	if headers  != nil {
		for k, v := range headers {
			req.Header.Set(k, v)
		}
	}
	upstream := p.Match(req)
	if upstream == nil {
		return nil, ErrRouterNotFound
	}
	return DoUpstream(upstream,req)
}

type Handler interface {
	ServeHTTP(http.ResponseWriter, *http.Request) (int, error)
}
