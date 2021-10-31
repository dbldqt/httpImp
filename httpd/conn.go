package httpd

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net"
)

type conn struct {
	svr *Server
	rawConn net.Conn
	bufw *bufio.Writer				//使用写缓冲，减少系统调用
	limitR *io.LimitedReader		//为了限制首部字节数量，防止请求中设置太多的首部字节造成服务器解析压力，形成恶意攻击，读取超过限制数量时返回io.EOF
	bufr *bufio.Reader				//使用bufer.Reader可以支持readLine方法
}

func newConn(rawConn net.Conn,svr *Server) *conn {
	lr := &io.LimitedReader{
		R: rawConn,
		N: 1<<20,
	}
	return &conn{
		svr:svr,
		rawConn: rawConn,
		bufw:bufio.NewWriterSize(rawConn,4<<10),
		limitR:lr,
		bufr:bufio.NewReaderSize(lr,4<<10),
	}
}

func (c *conn)Serve(){
	defer func() {
		if err := recover();err != nil{
			log.Printf("panic recoverred,err:%v\n",err)
		}
		c.rawConn.Close()
	}()
	//http1.1支持keep-alive长链接，所以一个连接中可能读出多个请求
	//多个请求，因此用for循环读取
	for{
		req,err := c.readRequest()
		if err != nil{
			handleErr(err,c)
			return
		}

		resp := c.setupResponse(req)
		c.svr.Handler.ServeHTTP(resp,req)
		if err = req.finishRequest(resp);err != nil{
			return
		}
		if resp.closeAfterReply {
			return
		}
	}
}

func (c *conn)readRequest() (*Request,error){
	return readRequest(c)
}

func (c *conn)setupResponse(req *Request)*response{
	return setupResponse(c,req)
}

func (c *conn)close(){
	c.rawConn.Close()
}

func handleErr(err error,c *conn){
	fmt.Println(err.Error())
}
