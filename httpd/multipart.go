package httpd

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"strings"
)

const bufSize = 4096

/**
每一部分之间以\r\n--boundary作为分隔符(delimiter)，
其中boundary在首部的第3行给出，表单的末尾是以\r\n--boundary--为结束。
要注意的是分隔符是\r\n--boundary，相较于boundary前面多了两个破折号(dash)。
 */
type MultipartReader struct {
	bufr *bufio.Reader				//所有的part共享这个bufr
	//记录bufr的读取过程中是否出现io.EOF错误，
	//如果发生了这个错误，则不应该再从bufr的底层Reader中读数据
	occurEofErr          bool
	crlfDashBoundaryDash []byte //\r\n--=boundary--
	crlfDashBoundary     []byte //\r\n--boundary,分隔符
	dashBoundary         []byte //--boundary
	dashBoundaryDash     []byte //--boundary--
	//因为所有Part都会在bufr上读取数据，前面part将属于它的数据消费掉之后，后续的part才能读取自己的数据。
	//因此我们用curPart记录当前是哪个part占有了bufr，方便我们对其管理。
	curPart	*Part					//当前读取到哪个part
	crlf 	[2]byte					//用于消费掉\r\n
}

//传入的r将是Request的Body
func NewMultipartReader(r io.Reader,boundary string) *MultipartReader{
	b := []byte("\r\n--" + boundary + "--")
	return &MultipartReader{
		bufr:                 bufio.NewReaderSize(r,bufSize),	//将io.Reader封装成bufio.Reader
		crlfDashBoundaryDash: b,
		crlfDashBoundary:     b[:len(b)-2],
		dashBoundary:         b[2:len(b)-2],
		dashBoundaryDash:     b[2:],
	}
}

// https://www.gufeijun.com
func (mr *MultipartReader) NextPart() (p *Part, err error) {
	if mr.curPart != nil {
		//将当前的Part关闭掉，否则无法建立新的part
		if err = mr.curPart.Close(); err != nil {
			return
		}
		if err = mr.discardCRLF(); err != nil {
			return
		}
	}
	line, err := mr.readLine()
	if err != nil {
		return
	}
	if bytes.Equal(line, mr.dashBoundaryDash) {
		return nil, io.EOF
	}
	if !bytes.Equal(line, mr.dashBoundary) {
		err = fmt.Errorf("want delimiter %s, but got %s", mr.dashBoundary, line)
		return
	}
	p = new(Part)
	p.mr = mr
	if err = p.readHeader(); err != nil {
		return
	}
	mr.curPart = p
	return
}

func (mr *MultipartReader) discardCRLF() (err error) {
	if _, err = io.ReadFull(mr.bufr, mr.crlf[:]); err == nil {
		if mr.crlf[0] != '\r' && mr.crlf[1] != '\n' {
			err = fmt.Errorf("expect crlf, but got %s", mr.crlf)
		}
	}
	return
}

func (mr *MultipartReader) readLine() ([]byte, error) {
	return readLine(mr.bufr)
}

func (p *Part) readHeader() (err error) {
	p.Header, err = readHeader(p.mr.bufr)
	return err
}

func (p *Part) Close() error {
	if p.closed {
		return nil
	}
	_, err := io.Copy(ioutil.Discard, p)
	p.closed = true
	return err
}

type Part struct {
	Header           Header
	mr               *MultipartReader
	formName         string
	fileName         string
	closed           bool			//part是否关闭
	//substituteReader，如果它不为空，我们对Part的Read则优先交给substituteReader处理，
	//主要是为了方便引入io.LimiteReader来凝练我们的代码。
	substituteReader io.Reader		//替补Reader
	parsed           bool			//是否已经解析过formName以及fileName
}

// https://www.gufeijun.com
func (p *Part) Read(buf []byte) (n int, err error) {
	if p.closed {
		return 0, io.EOF
	}
	if p.substituteReader != nil {
		return p.substituteReader.Read(buf)
	}
	bufr := p.mr.bufr
	var peek []byte
	if p.mr.occurEofErr {	//如果已经出现EOF错误，我们只需要关心bufr已经缓存的数据即可
		peek, _ = bufr.Peek(bufr.Buffered())
	} else {
		//bufSize即bufr的缓存大小，强制触发底层io.Reader的io，填满bufr缓存
		peek, err = bufr.Peek(bufSize)
		//出现EOF错误，代表底层Reader已经没有足够的数据填满bufr的缓存，我们利用递归跳转到另一个if分支
		if err == io.EOF {
			p.mr.occurEofErr = true
			return p.Read(buf)
		}
		if err != nil {
			return 0, err
		}
	}
	//在peek出的数据中找boundary
	index := bytes.Index(peek, p.mr.crlfDashBoundary)
	//两种情况：
	//1.在peek出的数据中找到分隔符，顺利找到了该part的Read指针终点。
	//2.出现了EOF错误，且剩余的buf缓存中没有分隔符，说明报文未发送完全，客户端主动关闭了连接，我们提前终止Read
	if index != -1 || (index == -1 && p.mr.occurEofErr) {
		p.substituteReader = io.LimitReader(bufr, int64(index))
		return p.substituteReader.Read(buf)
	}
	//以下则是在peek出的数据中没有找到分隔符的情况，说明peek出的数据属于当前的part

	//见上文讲解，不能一次把所有的bufSize都当作消息主体读出，还需要减去分隔符的最长子串的长度。
	maxRead := bufSize - len(p.mr.crlfDashBoundary) + 1
	if maxRead > len(buf) {
		maxRead = len(buf)
	}
	return bufr.Read(buf[:maxRead])
}

func (p *Part) FormName() string {
	if !p.parsed {
		p.parseFormData()
	}
	return p.formName
}

func (p *Part) FileName() string {
	if !p.parsed {
		p.parseFormData()
	}
	return p.fileName
}

func (p *Part) parseFormData() {
	p.parsed = true
	cd := p.Header.Get("Content-Disposition")
	ss := strings.Split(cd, ";")
	if len(ss) == 1 || strings.ToLower(ss[0]) != "form-data" {
		return
	}
	for _, s := range ss {
		key, value := getKV(s)
		switch key {
		case "name":
			p.formName = value
		case "filename":
			p.fileName = value
		}
	}
}

func getKV(s string) (key string, value string) {
	ss := strings.Split(s, "=")
	if len(ss) != 2 {
		return
	}
	return strings.TrimSpace(ss[0]), strings.Trim(ss[1], `"`)
}