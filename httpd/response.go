package httpd

import (
	"bufio"
	"fmt"
)

type response struct {
	//http链接
	c *conn

	//是否已经调用过WriteHeader，防止重复调用
	wroteHeader bool
	header Header

	//WriterHeader传入的状态码，默认为200
	statusCode int

	//如果handler已经结束并且Write的长度未超过最大写缓存量，我们给头部自动设置Content-Length
	//如果handler未结束且Write的长度超过了最大写缓存量，我们使用chunk编码传输数据
	//会在finishRequest中，调用Flush之前将其设置成true
	handlerDone bool

	//bufw = bufio.NewBufioWriter(chunkWriter)
	bufw *bufio.Writer
	cw *chunkWriter

	req *Request

	//是否在本次http请求结束后关闭tcp链接，以下情况需要关闭链接
	//1、HTTP/1.1之前的版本
	//2、请求报文头部设置了Connection:close
	//3、在net.Conn进行Write的过程中发生错误
	closeAfterReply bool

	//是否使用chunk编码的方式，一旦检测到应该使用chunk编码，则会被chunkWriter设置成true
	chunking bool
}

//写入流的顺序：response => (*response).bufw => chunkWriter
// => (*chunkWriter).(*response).(*conn).bufw => net.Conn
func (w *response) Write(p []byte) (int,error) {
	n,err := w.bufw.Write(p)
	if err != nil {
		w.closeAfterReply = true
	}
	return n,err
}

func (w *response) Header() Header {
	return w.header
}

func (w *response) WriteHeader(statusCode int) {
	if w.wroteHeader {
		return
	}

	w.statusCode = statusCode
	w.wroteHeader = true
}
//我们框架的解决方案是规定最多缓存4KB数据，如果用户在handler中写入的量小于这个值，我们使用Content-Length，否则使用chunk编码的方式。
//chunk编码的解析效率会比Content-Length方式低上很多，同时也有控制信息等数据开销，我们要兼顾性能进行考虑,因此不直接全部用chunk方式
func setupResponse(c *conn,req *Request)*response {
	resp := &response{
		c:c,
		header: make(Header),
		statusCode: 200,
		req:req,
	}

	cw := &chunkWriter{resp: resp}
	resp.cw = cw
	//此处将cw作为bufw的底层writer传入，调用resp.bufw.Flush时，会将数据写入到cw中
	resp.bufw = bufio.NewWriterSize(cw,4096)
	var (
		protoMinor int
		protoMajor int
	)

	fmt.Sscanf(req.Proto,"HTTP/%d.%d",protoMajor,protoMinor)
	if protoMajor < 1 || protoMinor == 1 && protoMajor == 0 || req.Header.Get("Connection") == "close" {
		resp.closeAfterReply = true
	}
	return resp
}

