package httpd

import (
	"net"
)

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

type HandlerFunc func(w ResponseWriter,r *Request)

type Handler interface {
	ServeHTTP(w ResponseWriter,r *Request)
}

type ServeMux struct {
	m map[string]HandlerFunc
}

func NewServerMux() *ServeMux {
	return &ServeMux{
		m:make(map[string]HandlerFunc),
	}
}

func (sm *ServeMux)HandleFunc(pattern string,cb HandlerFunc) {
	if sm.m == nil {
		sm.m = make(map[string]HandlerFunc)
	}
	sm.m[pattern] = cb
}

func (sm *ServeMux) Handle(pattern string,handler Handler) {
	if sm.m == nil {
		sm.m = make(map[string]HandlerFunc)
	}
	sm.m[pattern] = handler.ServeHTTP
}

func (sm *ServeMux) ServeHTTP(w ResponseWriter, r *Request) {
	handler, ok := sm.m[r.Url.Path]
	if !ok {
		if len(r.Url.Path) > 1 && r.Url.Path[len(r.Url.Path)-1] == '/' {
			handler, ok = sm.m[r.Url.Path[:len(r.Url.Path)-1]]
		}
		if !ok {
			w.WriteHeader(StatusNotFound)
			return
		}
	}
	handler(w, r)
}

var defaultServeMux ServeMux

var DefaultServeMux = &defaultServeMux

func HandleFunc(pattern string, cb HandlerFunc) {
	DefaultServeMux.HandleFunc(pattern, cb)
}

func Handle(pattern string, handler Handler) {
	DefaultServeMux.Handle(pattern, handler)
}

func ListenAndServe(addr string, handler Handler) error {
	if handler == nil {
		handler = DefaultServeMux
	}
	svr := &Server{
		Addr:    addr,
		Handler: handler,
	}
	return svr.ListenAndServe()
}