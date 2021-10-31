package httpd

import (
	"fmt"
	"net/http"
	"strconv"
)

type ResponseWriter interface {
	Write([]byte)(int,error)			//
	Header() Header						//设置header头
	WriteHeader(statusCode int)			//写入状态码
}

//最后捋一下Write写入流的顺序：用户在handler中对ResponseWriter写 => 对response写 => 对response的bufw成员写 => bufw是chunkWriter的封装，
//对chunkWriter写 => 对(*chunkWriter).(*response).(*conn).bufw写 => 这个bufw是对net.Conn的封装，对net.Conn写。
type chunkWriter struct {
	resp *response

	//记录是否第一次调用Write方法
	wrote bool
}

func (cw *chunkWriter) Write(p []byte) (n int,err error){
	//第一次触发Write方法
	if !cw.wrote {
		cw.finalizeHeader(p)
		if err = cw.writeHeader(); err != nil {
			return
		}
		cw.wrote = true
	}
	bufw := cw.resp.c.bufw
	//当Writes数据超过缓存容量时，利用chunk编码传输
	if cw.resp.chunking {
		_,err := fmt.Fprintf(bufw,"%x\r\n",len(p))
		if err != nil {
			return	0,err
		}
	}
	n,err = bufw.Write(p)
	if err == nil && cw.resp.chunking {
		_,err = bufw.WriteString("\r\n")
	}
	return n,err
}

//设置响应头
func (cw *chunkWriter) finalizeHeader(p []byte) {
	header := cw.resp.header
	//如果用户未指定Content-Type,我们使用嗅探。此处直接使用标准库api
	if header.Get("Content-Type") == "" {
		header.Set("Content-Type",http.DetectContentType(p))
	}

	//如果用户未指定任何编码方式
	if header.Get("Content-Length") == "" && header.Get("Transfer-Encoding") == "" {
		//因为flush触发该write
		if cw.resp.handlerDone {
			buffered := cw.resp.bufw.Buffered()
			header.Set("Content-Length",strconv.Itoa(buffered))
		} else {
			//因为超出缓存触发Write
			cw.resp.chunking = true
			header.Set("Transfer-Encoding","chunked")
		}
		return
	}

	if header.Get("Transfer-Encoding") == "chunked" {
		cw.resp.chunking = true
	}
}

//将响应头部发送
func (cw *chunkWriter) writeHeader() (err error) {
	codeString := strconv.Itoa(cw.resp.statusCode)
	//statusText是个map,key为状态码，value为描述信息，见status.go,拷贝于标准库
	statusLine := cw.resp.req.Proto + " " + codeString + " " + statusText[cw.resp.statusCode] + "\r\n"
	bufw := cw.resp.c.bufw
	_,err = bufw.WriteString(statusLine)
	if err != nil {
		return
	}

	for key,value := range cw.resp.header {
		_,err = bufw.WriteString(key + ": " +value[0] + "\r\n")
		if err != nil {
			return
		}
	}
	_,err = bufw.WriteString("\r\n")
	return
}
