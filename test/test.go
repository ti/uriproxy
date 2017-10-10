package main

import (
	"log"
	"github.com/ti/uriproxy"
	"net/http"
	"time"
	"io/ioutil"
	"encoding/json"
)

func TestMultiUriClient()  {
	var result map[interface{}]int = make(map[interface{}]int)
	for i := 0; i< 99; i ++ {
		start := time.Now().Nanosecond() / 10000
		resp, err := uriproxy.DefaultClient.Request(http.MethodGet,"http://dev.lnx.cm:2001,dev.lnx.cm:12002,dev.lnx.cm:12003/you,dev.lnx.cm:2003/you", nil, nil )
		if err != nil {
			log.Println("ERROR", err)
			AddRet(result, 0)
			log.Println("runing",i, "ERROR", 0, "time", (time.Now().Nanosecond() / 10000) - start)

			time.Sleep(time.Second)

		} else {
			respBody, err := ioutil.ReadAll(resp.Body)
			if err  != nil {
				log.Println("ERROR", err)
				AddRet(result, 1)

			} else {
				resp.Body.Close()
				var ret struct {Host string}
				if err := json.Unmarshal(respBody, &ret); err == nil {
					AddRet(result, ret.Host)
				} else {
					log.Println("ERROR",err)
					AddRet(result, 3)
				}
			}
			AddRet(result, resp.StatusCode)
			log.Println("runing",i, "time", resp.StatusCode, "耗时", (time.Now().Nanosecond() / 10000) - start)
		}
	}
	log.Println("请求结果", result)
	time.Sleep(time.Minute)
	return
}

func TestMultiUriProxy()  {
	uri := "http://user:pass@dev.lnx.cm:2001,dev.lnx.cm:12004,dev.lnx.cm:12001,dev.lnx.cm:12000/web/go?header_upstream[OA-Accept]=delete&transparent=true"
	uriproxy.DefaultProxy.AddUpstream("/","", uri)
	uriproxy.DefaultProxy.AddUpstream("/no/",  "", "http://dev.lnx.cm:2001/no")

	go func() {
		time.Sleep(5 * time.Second)
		TestHttp()
	}()
	log.Println("Listening：", 9090)
	err := http.ListenAndServe(":9090",http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respfunc := func(res *http.Response) bool {
			body, err := ioutil.ReadAll(res.Body)
			log.Println(string(body), err)

			res.Body.Close()

			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"name":"hello"}`))
			return true

		}
		_ = respfunc
		uriproxy.DefaultProxy.ServeHTTP(w,r)

	}))
	if err != nil {
		panic(err)
	}
}

func TestSingleUriProxy()  {
	log.Println("Listening：", 9080)
	err := http.ListenAndServe(":9080",http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.URL.Host = "dev.lnx.cm:2001,dev.lnx.cm:12004,dev.lnx.cm:12001,dev.lnx.cm:12000"

		respfunc := func(res *http.Response) bool {
			body, err := ioutil.ReadAll(res.Body)
			if err != nil {
				w.WriteHeader(500)
				return true
			}

			_ = body

			res.Body.Close()

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(500)
			w.Write([]byte(`{"name":"hello"}`))
			return true

		}
		_ = respfunc

		code, err := uriproxy.DefaultClient.Proxy(w,r,respfunc)
		if err != nil {
			if code >= 400 {
				w.WriteHeader(code)
			} else {
				w.WriteHeader(502)
			}
			w.Write([]byte(err.Error()))
		}
	}))
	if err != nil {
		panic(err)
	}
}

func main() {
	TestSingleUriProxy()
}

func AddRet(ret map[interface{}]int, key interface{})  {
	cnt := ret[key]
	cnt ++
	ret[key] = cnt
}

var errCnt int

var errMap map[int]int = make(map[int]int)

func TestHttp()  {
	total := 10000
	for i :=0 ; i < total; i++ {

		resp, err := http.Get("http://localhost:9090/go/go/go")

		resp.Body.Close()

		if err != nil {
			errCnt ++

		} else {
			if resp.StatusCode != 200 {
				errCnt ++
				cnt := errMap[resp.StatusCode]
				cnt ++
				errMap[resp.StatusCode] = cnt
			}
			log.Println("第", i, "次 结果",resp.StatusCode, err)
		}
	}

	log.Println("报告错误：","总请求",total,"失败",errCnt, "失败分类",errMap)
}