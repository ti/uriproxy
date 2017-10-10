package uriproxy

import (
	"testing"
	"time"
	"net/http"
	"log"
	"io/ioutil"
	"encoding/json"
	"os"
)

func TestClient_Request(t *testing.T) {
	log.SetOutput(ioutil.Discard)
	defer log.SetOutput(os.Stderr)
	var result map[interface{}]int = make(map[interface{}]int)
	for i := 0; i< 9; i ++ {
		return
		start := time.Now().Nanosecond() / 10000
		resp, err := DefaultClient.Request(http.MethodGet,"http://dev.lnx.cm:2001,dev.lnx.cm:12002,dev.lnx.cm:12003/you,dev.lnx.cm:2003/you", nil, nil )
		if err != nil {
			log.Println("ERROR", err)
			addRet(result, 0)
			log.Println("runing",i, "ERROR", 0, "time", (time.Now().Nanosecond() / 10000) - start)

			time.Sleep(time.Second)

		} else {
			respBody, err := ioutil.ReadAll(resp.Body)
			if err  != nil {
				log.Println("ERROR", err)
				addRet(result, 1)

			} else {
				resp.Body.Close()
				var ret struct {Host string}
				if err := json.Unmarshal(respBody, &ret); err == nil {
					addRet(result, ret.Host)
				} else {
					log.Println("ERROR",err)
					addRet(result, 3)
				}
			}
			addRet(result, resp.StatusCode)
			log.Println("runing",i, "time", resp.StatusCode, "耗时", (time.Now().Nanosecond() / 10000) - start)
		}
	}
	log.Println("RESULT", result)
	time.Sleep(time.Minute)
}



func addRet(ret map[interface{}]int, key interface{})  {
	cnt := ret[key]
	cnt ++
	ret[key] = cnt
}
