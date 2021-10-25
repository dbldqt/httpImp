package httpd

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/url"
	"strconv"
	"strings"
)

type Request struct {
	Method string	//请求方法，如POST、GET
	Url *url.URL	//Url
	Proto string 	//协议版本
	Header Header	//首部字段
	Body io.Reader	//用于读取保温主题
	RemoteAddr string	//客户端地址
	RequestURI	string	//字符串形式的url
	conn *conn
	cookies map[string]string	//存储cookies
	queryString map[string]string //存储查询字符串
}
//公共方法获取查询字符串
func (r *Request) Query(name string) string{
	return r.queryString[name]
}

func (r *Request) Cookie(name string) string{
	//cookie采用懒加载方式，使用时再分配能存及处理，不使用则不处理，提高性能
	if r.cookies == nil {
		r.parseCookies()
	}
	return r.cookies[name]
}

func (r *Request) parseQuery() {
	r.queryString = parseQuery(r.Url.RawQuery)
}

func parseQuery(rawQuery string) map[string]string{
	parts := strings.Split(rawQuery,"&")
	queries := make(map[string]string,len(parts))
	for _,part := range parts {
		index := strings.IndexByte(part,'=')
		if index == -1 || index == len(parts)+1{
			continue
		}
		queries[strings.TrimSpace(part[:index])] = strings.TrimSpace(part[index+1:])
	}
	return queries
}

func (r *Request) parseCookies() {
	if r.cookies != nil{
		return
	}

	r.cookies = make(map[string]string)
	rawCookies,ok := r.Header["Cookie"]
	if !ok{
		return
	}

	for _,line := range rawCookies{
		kvs := strings.Split(strings.TrimSpace(line),";")
		if len(kvs) == 1 && kvs[0] == ""{
			continue
		}

		for i := 0;i < len(kvs);i++{
			index := strings.IndexByte(kvs[i],'=')
			if index == -1 {
				continue
			}
			r.cookies[strings.TrimSpace(kvs[i][:index])] = strings.TrimSpace(kvs[i][index+1:])
		}
 	}

	return
}
//body的长度是个重要的问题，需要正确的读取，尤其是keep-alive情况下，不能出现超范围读取的情况
func (r *Request)setupBody(){
	//POST和PUT之外的请求不允许设置报文主体
	if r.Method != "POST" && r.Method != "PUT" {
		r.Body = &eofReader{}
		return
	}

	if r.chunked() {
		r.Body = &chunkReader{
			bufr: r.conn.bufr,
		}
		r.fixExpectContinueReader()
		return
	}
	/*
	Content-Length 字段必须真实反映实体长度，但实际应用中，有些时候实体长度并没那么好获得，例如实体来自于网络文件，或者由动态语言生成。
	这时候要想准确获取长度，只能开一个足够大的 buffer，等内容全部生成好再计算。但这样做一方面需要更大的内存开销，另一方面也会让客户端等更久。
	但在 HTTP 报文中，实体一定要在头部之后，顺序不能颠倒，
	为此我们需要一个新的机制：不依赖头部的长度信息，也能知道实体的边界。然后内容可以分块逐步传输。Transfer-Encoding: chunked
	 */
	cl := r.Header.Get("Content-Length")
	//读取不到报文长度无法界定body，也要返回eofReader
	if cl == "" {
		r.Body = &eofReader{}
		return
	}

	contentLength,err := strconv.ParseInt(cl,10,64)
	if err != nil{
		r.Body = &eofReader{}
		return
	}

	r.Body = &io.LimitedReader{
		R: r.conn.bufr,			//因为之前已经解析出报头部以及空行，所以读取起始点已经设置在主体起始位置
		N: contentLength,
	}
	r.fixExpectContinueReader()
}
//为了防止资源的浪费，有些客户端在发送完http首部之后，发送body数据前，会先通过发送Expect: 100-continue查询服务端是否希望接受body数据，
//服务端只有回复了HTTP/1.1 100 Continue客户端才会再次发送body。因此我们也要处理这种情况：
func (r *Request) fixExpectContinueReader() {
	if r.Header.Get("Expect") != "100-continue" {
		return
	}
	r.Body = &expectContinueReader{
		r: r.Body,
		w:r.conn.bufw,
	}
}

func (r *Request)chunked() bool{
	te := r.Header.Get("Transfer-Encoding")
	return te == "chunked"
}

/**
	如果用户Handler中没有去读取Body的数据，就意味着处理同一个socket连接上的下一个http报文时，Body未消费的数据会干扰下一个报文的解析。
	所以我们的框架还需要在Handler结束后，将当前http请求的数据给消费掉。给Request增加一个finishRequest方法，以后的一些善尾工作都将交给它：
 */
func (r *Request) finishRequest() (err error){
	//将缓存中的剩余的数据发送到rwc中
	if err=r.conn.bufw.Flush();err!=nil{
		return
	}
	//消费掉剩余的数据
	_,err = io.Copy(ioutil.Discard,r.Body)
	return err
}

/**
	这里主要解析url,header，cookie使用懒加载的方式，后续再处理
 */
func readRequest(c *conn) (*Request,error) {
	r := new(Request)
	r.conn = c
	r.RemoteAddr = c.rawConn.RemoteAddr().String()
	//读出第一行，如Get /index?name=gu HTTP/1.1
	line,err := readLine(c.bufr)
	if err != nil{
		return nil,err
	}
	_,err = fmt.Sscanf(string(line),"%s%s%s",&r.Method,&r.RequestURI,&r.Proto)  //将空白分隔的值按指定格式存入指定变量
	if err != nil{
		return nil,err
	}

	r.Url,err = url.ParseRequestURI(r.RequestURI)
	if err != nil{
		return nil,err
	}
	//解析qureyString
	r.parseQuery()
	//读header
	r.Header,err = readHeader(c.bufr)
	if err != nil{
		return nil, err
	}
	const noLimit = (1 << 63)-1
	r.conn.limitR.N = noLimit		//body的读取无需进行读取字符数限制
	//设置body
	r.setupBody()
	return r,nil
}

func readLine(bufr *bufio.Reader) ([]byte,error){
	p,isPrefix,err := bufr.ReadLine()
	if err != nil{
		return p,err
	}

	var l []byte
	for isPrefix {
		l,isPrefix,err = bufr.ReadLine()
		if err != nil{
			break
		}
		p = append(p,l...)
	}

	return p,err
}

func readHeader(bufr *bufio.Reader) (Header,error){
	header := make(Header)
	for {
		line,err := readLine(bufr)
		if err != nil{
			return nil,err
		}
		//如果读到/r/n/r/n，代表报文首部结束
		//readLine方法返回换行符之前的行内容，空行自然没有长度
		if len(line) == 0{
			break
		}
		//example：Connection :keep-alive
		i := bytes.IndexByte(line,':')
		if i == -1{
			return nil,errors.New("unsupported protocol")
		}

		if i == len(line)-1 {
			continue
		}
		k,v := string(line[:i]),strings.TrimSpace(string(line[i+1:]))
		header[k] = append(header[k],v)
	}
	return header,nil
}