package uriproxy

import (
	"path"
	"strings"
	"net/http"
	"net/url"
	"os"
	"net"
	"time"
	"strconv"
	"net/http/httputil"
	"io/ioutil"
	"errors"
)

var (
	ErrMaxBytesExceeded = errors.New("http: request body too large")
	ErrNoAvailableUpstream =  errors.New("no hosts available upstream")
	ErrRouterNotFound =  errors.New("404 router not found")
	ErrInternalServerError =  errors.New("500 internal server error")
)

func newReplacer(r *http.Request, emptyValue string) *replacer {
	return &replacer{
		request:            r,
		customReplacements: make(map[string]string),
	}
}

type replacer struct {
	customReplacements map[string]string
	request            *http.Request
	emptyValue         string
}

// Replace performs a replacement of values on s and returns
// the string with the replaced values.
func (r *replacer) Replace(s string) string {
	// Do not attempt replacements if no placeholder is found.
	if !strings.ContainsAny(s, "{}") {
		return s
	}
	result := ""
	for {
		idxStart := strings.Index(s, "{")
		if idxStart == -1 {
			// no placeholder anymore
			break
		}
		idxEnd := strings.Index(s[idxStart:], "}")
		if idxEnd == -1 {
			// unpaired placeholder
			break
		}
		idxEnd += idxStart

		// get a replacement
		placeholder := s[idxStart : idxEnd+1]
		replacement := r.getSubstitution(placeholder)

		// append prefix + replacement
		result += s[:idxStart] + replacement

		// strip out scanned parts
		s = s[idxEnd+1:]
	}

	// append unscanned parts
	return result + s
}

// getSubstitution retrieves value from corresponding key
func (r *replacer) getSubstitution(key string) string {
	// search custom replacements first
	if value, ok := r.customReplacements[key]; ok {
		return value
	}

	// search request headers then
	if key[1] == '>' {
		want := key[2 : len(key)-1]
		for key, values := range r.request.Header {
			// Header placeholders (case-insensitive)
			if strings.EqualFold(key, want) {
				return strings.Join(values, ",")
			}
		}
	}
	// next check for cookies
	if key[1] == '~' {
		name := key[2 : len(key)-1]
		if cookie, err := r.request.Cookie(name); err == nil {
			return cookie.Value
		}
	}
	// next check for query argument
	if key[1] == '?' {
		query := r.request.URL.Query()
		name := key[2 : len(key)-1]
		return query.Get(name)
	}

	// search default replacements in the end
	switch key {
	case "{method}":
		return r.request.Method
	case "{scheme}":
		if r.request.TLS != nil {
			return "https"
		}
		return "http"
	case "{hostname}":
		name, err := os.Hostname()
		if err != nil {
			return r.emptyValue
		}
		return name
	case "{host}":
		return r.request.Host
	case "{hostonly}":
		host, _, err := net.SplitHostPort(r.request.Host)
		if err != nil {
			return r.request.Host
		}
		return host
	case "{path}":
		return r.request.URL.Path
	case "{path_escaped}":
		return url.QueryEscape(r.request.URL.Path)
	case "{query}":
		return r.request.URL.RawQuery
	case "{query_escaped}":
		return url.QueryEscape(r.request.URL.RawQuery)
	case "{fragment}":
		return r.request.URL.Fragment
	case "{proto}":
		return r.request.Proto
	case "{remote}":
		host, _, err := net.SplitHostPort(r.request.RemoteAddr)
		if err != nil {
			return r.request.RemoteAddr
		}
		return host
	case "{port}":
		_, port, err := net.SplitHostPort(r.request.RemoteAddr)
		if err != nil {
			return r.emptyValue
		}
		return port
	case "{uri}":
		return r.request.URL.RequestURI()
	case "{uri_escaped}":
		return url.QueryEscape( r.request.URL.RequestURI())
	case "{rewrite_uri}":
		return r.request.URL.RequestURI()
	case "{rewrite_uri_escaped}":
		return url.QueryEscape(r.request.URL.RequestURI())
	case "{when}":
		return time.Now().Format(time.RFC3339)
	case "{when_unix}":
		return strconv.FormatInt(time.Now().Unix(), 10)
	case "{file}":
		_, file := path.Split(r.request.URL.Path)
		return file
	case "{dir}":
		dir, _ := path.Split(r.request.URL.Path)
		return dir
	case "{request}":
		dump, err := httputil.DumpRequest(r.request, false)
		if err != nil {
			return r.emptyValue
		}
		return requestReplacer.Replace(string(dump))
	case "{request_body}":
		b, err := ioutil.ReadAll(r.request.Body)
		if err != nil {
			if err == ErrMaxBytesExceeded {
				return r.emptyValue
			}
		}
		return requestReplacer.Replace(string(b))
	}

	return r.emptyValue
}


// requestReplacer is a strings.Replacer which is used to
// encode literal \r and \n characters and keep everything
// on one line
var requestReplacer = strings.NewReplacer(
	"\r", "\\r",
	"\n", "\\n",
)

//isPathInBasePath check if path is in the child of base path
//exp: /first/your/path is in /first/
func IsPathMatchBasePath(src, base string) bool {
	if base == "/" || base == "" {
		return true
	}
	// sanitize the paths for comparison, very important
	// (slightly lossy if the base path requires multiple
	// consecutive forward slashes, since those will be merged)
	pHasTrailingSlash := strings.HasSuffix(src, "/")
	baseHasTrailingSlash := strings.HasSuffix(base, "/")
	src = path.Clean(src)
	base = path.Clean(base)
	if pHasTrailingSlash {
		src += "/"
	}
	if baseHasTrailingSlash {
		base += "/"
	}

	return strings.HasPrefix(src, base)
}
