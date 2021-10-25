package httpd

import (
	"bufio"
	"errors"
	"io"
)

type eofReader struct {
}

func (er *eofReader)Read([]byte)(n int,err error){
	return 0,io.EOF
}

type chunkReader struct {
	bufr *bufio.Reader
	crlf [2]byte	//用来读取\r\n
	done bool		//标志是否读取完毕
	n int			//当前正在处理的块中未读字节数
}

func (cr *chunkReader)getChunkSize() (chunkSize int,err error) {
	line,err := readLine(cr.bufr)
	if err != nil{
		return
	}

	//chunk编码长度为16进制，此处需要转换为10进制
	for i:=0;i < len(line);i++ {
		switch {
		case 'a' <= line[i] && line[i] <= 'f':
			chunkSize = chunkSize*16 + int(line[i]-'a') + 10
		case 'A' <= line[i] && line[i] <= 'F':
			chunkSize = chunkSize*16 + int(line[i]-'A') + 10
		case '0' <= line[i] && line[i] <= '9':
			chunkSize = chunkSize*16 + int(line[i]-'0')
		default:
			return 0, errors.New("illegal hex number")
		}
	}
	return
}

func (cr *chunkReader) discardCRLF() (err error){
	if _, err = io.ReadFull(cr.bufr, cr.crlf[:]); err == nil {
		if cr.crlf[0] != '\r' || cr.crlf[1] != '\n' {
			return errors.New("unsupported encoding format of chunk")
		}
	}
	return
}

func (cr *chunkReader)Read(p []byte)(n int,err error){
	if cr.done {
		return
	}

	if cr.n == 0 {
		cr.n,err = cr.getChunkSize()
		if err != nil{
			return
		}
	}

	if cr.n == 0 {
		cr.done = true
		err = cr.discardCRLF()
		return
	}

	//如果当前块剩余的数据大于p的长度
	if len(p) <= cr.n {
		n,err = cr.bufr.Read(p)
		cr.n -= n
		return n,err
	}

	//如果当前块剩余的数据不够p的长度
	n,_ = io.ReadFull(cr.bufr,p[:cr.n])
	cr.n = 0
	//将\r\n从流中消费掉
	if err = cr.discardCRLF();err != nil {
		return
	}
	return
}

type expectContinueReader struct{
	wroteContinue bool
	r io.Reader
	w *bufio.Writer
}

func (er *expectContinueReader) Read(p []byte)(n int,err error){
	//第一次读取前发送100 continue
	if !er.wroteContinue{
		er.w.WriteString("HTTP/1.1 100 Continue\r\n\r\n")
		er.w.Flush()
		er.wroteContinue = true
	}
	return er.r.Read(p)
}