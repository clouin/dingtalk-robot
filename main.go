package main

import (
	"encoding/json"
	"io/ioutil"
	"log"
	"net/http"

	"dingtalk-robot/api/dingtalk"

	"github.com/gorilla/mux"
)

func robotSend(writer http.ResponseWriter, request *http.Request) {
	//获取请求 request 的路由变量，返回 map [string]string
	//vars := mux.Vars(request)

	var req map[string]string
	body, _ := ioutil.ReadAll(request.Body)
	//解析json编码的数据并将结果存入req指向的值
	json.Unmarshal(body, &req)

	msgType := req["msgtype"]

	resp := dingtalk.Request(msgType, body)

	//编码函数
	response, _ := json.Marshal(resp)

	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	writer.Write(response)

}

func main() {
	r := mux.NewRouter()
	r.HandleFunc("/robot/send", robotSend).Methods("POST")

	log.Printf("start listening on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", r))
}
