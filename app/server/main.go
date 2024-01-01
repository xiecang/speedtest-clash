package main

import (
	"encoding/json"
	"fmt"
	"github.com/metacubex/mihomo/log"
	"github.com/xiecang/speedtest-clash/speedtest"
	"io"
	"net/http"
	"strconv"
	"time"
)

func resError(w http.ResponseWriter, err error) {
	w.WriteHeader(http.StatusInternalServerError)
	_, e := w.Write([]byte(fmt.Sprintf("{\"msg\": \"%s\"}", err.Error())))
	if e != nil {
		log.Errorln("write error: %v", e)
	}
}

func filterAlive(w http.ResponseWriter, req *http.Request) {
	if req.Method != "POST" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var (
		body speedtest.Options
	)
	bodyBytes, err := io.ReadAll(req.Body)
	log.Infoln("receive bodyBytes: %v", string(bodyBytes))
	err = json.Unmarshal(bodyBytes, &body)
	if err != nil {
		log.Errorln("read body error: body:<%+v>  err:%v", body, err)
		resError(w, err)
		return
	}
	if body.Timeout <= time.Second {
		body.Timeout = time.Second * 5
	}
	t, err := speedtest.NewTest(body)
	if err != nil {
		log.Errorln("new test error: %v", err)
		resError(w, err)
		return
	}
	_, err = t.TestSpeed()
	if err != nil {
		log.Errorln("test speed error: %v", err)
		resError(w, err)
		return
	}
	res, err := t.AliveProxiesToJson()
	if err != nil {
		log.Errorln("alive proxies to json error: %v", err)
		resError(w, err)
		return
	}
	t.LogNum()
	//t.LogAlive()
	w.Write(res)
}

func main() {

	http.HandleFunc("/api/clash_speedtest/v1/filter_alive", filterAlive)
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Header().Add("Content-Type", "text/html")
		w.Write([]byte(`<h1>SpeedTest Works</h1>`))
	})
	http.HandleFunc("/liveness", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	zeroBytes := make([]byte, 32*1024)
	for i := 0; i < len(zeroBytes); i++ {
		zeroBytes[i] = '0'
	}

	http.HandleFunc("/_down", func(w http.ResponseWriter, r *http.Request) {
		byteSize, err := strconv.Atoi(r.URL.Query().Get("bytes"))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(err.Error()))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Header().Add("Content-Disposition", "attachment; filename=largefile")
		w.Header().Add("Content-Type", "application/octet-stream")

		batchCount := byteSize / len(zeroBytes)
		for i := 0; i < batchCount; i++ {
			w.Write(zeroBytes)
		}
		w.Write(zeroBytes[:byteSize%len(zeroBytes)])
	})

	log.Infoln("Server started at http://localhost:8070")
	err := http.ListenAndServe(":8070", nil)
	log.Fatalln("%v", err)
}
