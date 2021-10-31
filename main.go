package main

import (
	"fmt"
	"github.com/dbldqt/httpImp/httpd"
	"io"
	"io/ioutil"
	"os"
)

type myHandler struct{}

func (*myHandler) ServeHTTP(w httpd.ResponseWriter, r *httpd.Request) {
	if r.Url.Path == "/photo"{
		file,err:=os.Open("./test.jpg")
		if err!=nil{
			fmt.Println("open file error:",err)
			return
		}
		io.Copy(w,file)
		file.Close()
		return
	}
	data,err:=ioutil.ReadFile("./test.html")
	if err!=nil{
		fmt.Println("readFile test.html error: ",err)
		return
	}
	w.Write(data)
}

func main() {
	svr := &httpd.Server{
		Addr:    "127.0.0.1:8080",
		Handler: new(myHandler),
	}
	panic(svr.ListenAndServe())
}