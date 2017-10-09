package main

import (
	"github.com/ti/ctxrouter"
	"github.com/ti/uriproxy"
	"net/http"
	"strings"
)


type Proxy struct {
	Router *ctxrouter.Router
	uriproxy.Proxy
}

var DefaultProxy = Proxy{
	Router: ctxrouter.New(),
}

// AddUpstream add upstream by pattern and method
func (p *Proxy) AddUpstream(pattern, method, uri string) error {
	if p.Router == nil  {
		p.Router = ctxrouter.New()
	}
	upstream, err := uriproxy.NewStaticUpstream(uri)
	if err != nil {
		return err
	}
	err = p.Router.Add(pattern, method, upstream)
	return err
}

// ServeHTTP satisfies the httpserver.Handler interface.
func (p *Proxy) Match(r *http.Request) uriproxy.Upstream {
	v, fix := p.Router.Match(r.Method, r.URL.Path)
	if u,  ok :=  v.V.(uriproxy.Upstream); ok {
		if len(fix) != 0 {
			r.URL.Path = fixPath(r.URL.Path, v.Pattern, "todo", fix)
		}
		return u
	}
	return nil
}

//
// fix path with patter
// exp: 1. reqPath: /data/v1/a.json, patter:/data/*path, path:/example/*path then, fixed path for proxy is /example/v1/data.json
// 	2. reqPath: /data.json patter:data.json, path:/example/v1/a.json, fixed path for proxy is /example/v1/data.json
//      3. reqPath: /data/me/v1/notice patter: /data/v1/:uid/notice,  fixed path for proxy is /data/v1/{user_id}/notice

func fixPath(reqPath, pathFix, uid string, p []string) string {
	//如果参数不足以满足fix条件，尝试替换uid
	if strings.Count(pathFix, ":")-len(p) == 1 {
		if uid == "" {
			uid = "null"
		}
		pathFix = strings.Replace(pathFix, ":uid", uid, 1)
	}

	if strings.Count(pathFix, ":")-len(p) == 1 && uid != "" {
		pathFix = strings.Replace(pathFix, ":uid", uid, 1)
	}

	if pathFix == "" {
		return reqPath
	}
	if !strings.Contains(pathFix, ":") && !strings.Contains(pathFix, "*") {
		return pathFix
	}
	var tplIndexes []int
	isNedNext := false
	for i := 0; i < len(pathFix); i++ {
		if pathFix[i] == '*' {
			return pathFix[0:i-1] + p[0]
		}
		if pathFix[i] == ':' {
			tplIndexes = append(tplIndexes, i)
			isNedNext = true
		}
		if isNedNext && pathFix[i] == '/' {
			tplIndexes = append(tplIndexes, i)
			isNedNext = false
		}
	}
	lenIndex := len(tplIndexes)
	if lenIndex == 1 {
		return pathFix[0:tplIndexes[0]] + p[0]
	}
	result := ""
	preIndex := 0
	paramsIndex := 0
	isPrefix := (pathFix[tplIndexes[lenIndex-1]:])[0] == ':'
	for i := 0; i < lenIndex; i++ {
		if i == lenIndex-1 {
			if isPrefix {
				result += pathFix[tplIndexes[i-1]:tplIndexes[i]] + p[paramsIndex]
			} else {
				result += pathFix[tplIndexes[i]:]
			}
			return result
		}
		if i%2 > 0 {
			preIndex = tplIndexes[i]
			continue
		}
		result += (pathFix[preIndex:tplIndexes[i]] + p[paramsIndex])
		preIndex = tplIndexes[i]
		paramsIndex++
	}

	return result
}
