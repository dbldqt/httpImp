package httpd

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/url"
	"os"
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
	contentType string
	boundary string

	postForm map[string]string
	multipartForm *MultipartForm
	haveParsedForm	bool
	parseFormErr error
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
func (r *Request) finishRequest(resp *response) (err error){
	//用户获取MultipartForm之后，应该调用RemoveAll方法将暂存的文件删除。用户可能在Handler中忘记调用RemoveALL，因此我们在Request的finishRequest方法中做出防备
	if r.multipartForm != nil{
		r.multipartForm.RemoveAll()
	}
	//告诉chunkWriter handler已经结束
	resp.handlerDone = true
	//触发chunkWriter的Writer方法，Write方法通过handlerDone来决定是用chunk还是Content-Length
	if err = resp.bufw.Flush();err != nil {
		return err
	}
	//如果是使用chunk编码，还需要将结束标识符传输
	if resp.chunking {
		_,err = resp.c.bufw.WriteString("0\r\n\r\n")
		if err != nil {
			return err
		}
	}

	//如果用户的handler中未Write任何数据，我们手动触发(*chunkWriter).writeHeader
	if !resp.cw.wrote {
		resp.header.Set("Content-Length","0")
		if err = resp.cw.writeHeader(); err != nil {
			return
		}
	}
	//将缓存中的剩余的数据发送到rwc中
	if err = r.conn.bufw.Flush();err!=nil{
		return
	}
	//消费掉剩余的数据
	_,err = io.Copy(ioutil.Discard,r.Body)
	return err
}
//文件的读取还是比较麻烦，用户还需要对MultipartForm的具体结构进行了解才能使用。我们对其简化：
func (r *Request) FormFile(key string)(fh* FileHeader,err error){
	mf,err := r.MultipartForm()
	if err!=nil{
		return
	}
	fh,ok:=mf.File[key]
	if !ok{
		return nil,errors.New("http: missing multipart file")
	}
	return
}

/**
	这里主要解析url,header，cookie则使用懒加载的方式，使用时再处理
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

	r.parseContentType()

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

func (r *Request)parseContentType(){
	ct := r.Header.Get("Content-Type")
	//Content-Type: multipart/form-data; boundary=------974767299852498929531610575
	//Content-Type: application/x-www-form-urlencoded
	index := strings.IndexByte(ct,';')
	if index == -1 {
		r.contentType = ct
	}

	if index == len(ct)-1 {
		return
	}

	ss := strings.Split(ct[index+1:],"=")
	if len(ss) < 2 || strings.TrimSpace(ss[0]) != "boundary" {
		return
	}
	r.contentType,r.boundary = ct[:index],strings.Trim(ss[1],"=")
	return
}

func (r *Request) MultipartReader()(*MultipartReader,error){
	if r.boundary==""{
		return nil,errors.New("no boundary detected")
	}
	return NewMultipartReader(r.Body,r.boundary),nil
}

func (r *Request) PostForm(name string) string {
	if !r.haveParsedForm {
		r.parseFormErr = r.parseForm()
	}
	if r.parseFormErr != nil  || r.postForm == nil {
		return ""
	}
	return r.postForm[name]
}

func (r *Request) MultipartForm() (*MultipartForm,error) {
	if !r.haveParsedForm {
		if err := r.parseForm();err != nil {
			r.parseFormErr = err
			return nil,err
		}
	}
	return r.multipartForm,r.parseFormErr
}

func (r *Request) parseForm() error{
	if r.Method != "POST" && r.Method != "PUT" {
		return errors.New("missing form body")
	}

	r.haveParsedForm = true
	switch r.contentType {
		case "application/x-www-form-urlencoded":
			return r.parsePostForm()
		case "multipart/form-data":
			return r.parseMultipartForm()
		default:
			return errors.New("unsupported form type")
	}
}

func (r *Request) parsePostForm() error {
	data, err := ioutil.ReadAll(r.Body)
	if err != nil {
		return err
	}
	r.postForm = parseQuery(string(data))
	return nil
}

func (r *Request) parseMultipartForm() error {
	mr,err := r.MultipartReader()
	if err != nil{
		return  err
	}
	r.multipartForm,err = mr.ReadForm()
	//让PostForm方法也可以访问multipart表单的文本数据
	r.postForm = r.multipartForm.Value
	return nil
}

type MultipartForm struct {
	Value map[string]string
	File  map[string]*FileHeader
}

//由于multipart表单可以上传文件，文件可能会很大，如果把用户上传的文件全部缓存在内存里，
//是极为消耗资源的。所以我们采取的机制是，规定一个内存里缓存最大量
//如果当前缓存量未超过这个值，我们将这些数据存到content这个字节切片里去。
//如果超过这个最大值，我们则将客户端上传文件的数据暂时存储到硬盘中去，待用户需要时再读取出来。tmpFile是这个暂时文件的路径。
type FileHeader struct {
	Filename string
	Header   Header
	Size     int
	content  []byte
	tmpFile  string
}

func (fh *FileHeader) Open() (io.ReadCloser,error) {
	if fh.inDisk(){
		//存储在硬盘上的情况，用户在读完这个文件之后有义务将这个文件关闭，所以我们的返回值是一个ReadCloser而不是单纯一个Reader。
		return os.Open(fh.tmpFile)
	}

	b := bytes.NewReader(fh.content)
	//对于存储在内存里的情况，我们将content切片转为一个bytes.Reader之后，并不需要Close方法，但为了保证编译通过，
	//我们使用ioutil.NopCloser函数给我们的Reader添加一个什么都不做的Close方法，来保证一致性
	return ioutil.NopCloser(b),nil
}

func (fh *FileHeader) inDisk() bool {
	return fh.tmpFile != ""
}

//有很多时候，用户希望将客户端上传的文件保存到硬盘的某个位置。用户拿到一个FileHeader后，
//还需要调用Open方法，Create一个文件，然后进行Copy，使用比较麻烦，我们给FileHeader增加一个Save方法：
func (fh *FileHeader) Save(dest string)(err error){
	rc,err:=fh.Open()
	if err!=nil{
		return
	}
	defer rc.Close()
	file,err:=os.Create(dest)
	if err!=nil{
		return
	}
	defer file.Close()
	_,err = io.Copy(file,rc)
	if err!=nil{
		os.Remove(dest)
	}
	return
}



