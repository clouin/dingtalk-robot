package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"dingtalk-robot/config"
	"dingtalk-robot/dingtalk/robot"

	"github.com/gorilla/mux"
	log "github.com/sirupsen/logrus"
)

func init() {
	config.LoadConfig()

	// Logrus has seven logging levels: Trace, Debug, Info, Warning, Error, Fatal and Panic.
	level, err := log.ParseLevel(config.Content.Log.Level)
	if err == nil {
		log.SetLevel(level)
	} else {
		log.SetLevel(log.InfoLevel)
	}
	log.Debugf(fmt.Sprintf("config.yaml: %+v", config.Content))
}

func robotSend(writer http.ResponseWriter, request *http.Request) {
	//获取请求 request 的路由变量，返回 map [string]string
	//vars := mux.Vars(request)

	body, _ := io.ReadAll(request.Body)

	resp := robot.Request(body)

	//编码函数
	response, _ := json.Marshal(resp)

	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	writer.Write(response)
}

func healthCheck(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("DingTalk robot is running"))
}

func main() {
	r := mux.NewRouter()
	r.HandleFunc("/robot/send", robotSend).Methods("POST")
	r.HandleFunc("/", healthCheck).Methods("GET")

	log.Printf("start listening on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", r))
}
