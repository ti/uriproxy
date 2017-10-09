# URI Proxy

Reverse Proxy and Http Client for distributed uri like http://host1,host2,host3/path

## Reverse Proxy
```go
    var proxy = urlproxy.DefaultProxy
	proxy.AddUpstream("/", "http://user:pass@dev.lnx.cm:2001,dev.lnx.cm:2002")
	proxy.AddUpstream("/no/",  "http://dev.lnx.cm:2003")
	
	proxy.ServeHTTP(w,r)

```


## URI Client

```go
resp, err := uriproxy.DefaultClient.Request("GET","http://dev.lnx.cm:2001,dev.lnx.cm:12002,dev.lnx.cm:12003/you,dev.lnx.cm:2003/your/url", nil, nil )
```