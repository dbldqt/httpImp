package httpd

type resonse struct {
	c *conn
}

type ResponseWriter interface {
	Write([]byte)(int,error)
}

func setupResponse(c *conn)*resonse {
	return &resonse{
		c:c,
	}
}

func (w *resonse) Write(p []byte)(int,error){
	return w.c.bufw.Write(p)
}
