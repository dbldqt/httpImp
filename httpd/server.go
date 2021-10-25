package httpd

import (
	"net"
)

type Handler interface {
	ServeHTTP(w ResponseWriter,r *Request)
}

type Server struct {
	Addr string
	Handler Handler
}

func (s *Server)ListenAndServe() error{
	l,err := net.Listen("tcp",s.Addr)
	if err != nil{
		return err
	}

	for {
		rawConn,err := l.Accept()
		if err != nil{
			continue
		}

		conn := newConn(rawConn,s)
		go conn.Serve()
	}
}