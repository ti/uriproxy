package uriproxy

import (
	"net/url"
	"net/http"
	"io"
	"sync"
	"strings"
	"sort"
	"encoding/base64"
)

// DefaultProxy is the default Client like nginx
var DefaultClient = &Client{sync.Map{},}

// Proxy represents a middleware instance that can proxy requests.
type Client struct {
	upstreams sync.Map
}

func NewClient() *Client {
	return  &Client{sync.Map{},}
}

// Rest clean all stored clients
func (c *Client) Rest(){
	c.upstreams.Range(func(key, value interface{}) bool {
		c.upstreams.Delete(key)
		return true
	})
}

// Request is request by proxy
func (c *Client) Request(method, uri string, headers map[string]string, body io.Reader) (*http.Response, error){
	URL, err := url.Parse(uri)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(method, uri, body)
	if err != nil {
		return  nil, err
	}
	if headers["Authorization"] == "" && URL.User != nil {
		req.Header.Set("Authorization",base64.StdEncoding.EncodeToString([]byte(URL.User.String())))
	}
	if headers  != nil {
		for k, v := range headers {
			req.Header.Set(k, v)
		}
	}
	upstream, err := c.LoadOrStoreUpstream(URL)
	if err != nil {
		return nil, err
	}
	return DoUpstream(upstream,req)
}

//LoadOrStoreUpstream
func (c *Client) LoadOrStoreUpstream(URL *url.URL) (Upstream, error) {
	hosts := strings.Split(URL.Host, ",")
	sort.Strings(hosts)
	key := strings.Join(hosts, ",")
	if v , ok := c.upstreams.Load(key); ok {
		if upstream, ok := v.(Upstream); ok {
			return upstream,nil
		} else {
			panic("clients just only contains  upstream")
		}
	} else {
		URL.Host = key
		URL.Path = ""
		upstream, err  := newStaticUpstreamByURL(URL)
		if err != nil {
			return nil, err
		}
		c.upstreams.Store(key, upstream)
		return upstream, nil
	}
}

//Proxy
func (c *Client) Proxy(w http.ResponseWriter, r *http.Request, respRespUpdateFn RespUpdateFn) (int, error) {
	upstream, err := c.LoadOrStoreUpstream(r.URL)
	if err != nil {
		return http.StatusInternalServerError, ErrInternalServerError
	}
	return ServeUpstream(upstream, w,r,respRespUpdateFn)
}
