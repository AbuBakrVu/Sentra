package server

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"sync"
)

// Сжатие ответов. Список устройств на большом парке весит мегабайты и
// запрашивается каждые 15 секунд; JSON из одинаковых ключей сжимается в
// 5–10 раз. Браузер распаковывает сам.

var gzPool = sync.Pool{New: func() any {
	w, _ := gzip.NewWriterLevel(io.Discard, 5) // 5: почти как 9 по размеру, заметно быстрее
	return w
}}

var compressible = []string{"application/json", "text/", "application/javascript", "image/svg+xml"}

const gzMinSize = 1024 // мелкие ответы сжимать бессмысленно

func withGzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") || r.Header.Get("Range") != "" ||
			strings.HasPrefix(r.URL.Path, "/download/") {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzWriter{ResponseWriter: w}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
}

type gzWriter struct {
	http.ResponseWriter
	gz      *gzip.Writer
	decided bool
	status  int
	buf     []byte // начало ответа, пока не ясно, стоит ли сжимать
}

func (g *gzWriter) WriteHeader(code int) {
	if g.status == 0 {
		g.status = code
	}
}

func (g *gzWriter) Write(p []byte) (int, error) {
	if g.status == 0 {
		g.status = http.StatusOK
	}
	if g.decided {
		if g.gz != nil {
			return g.gz.Write(p)
		}
		return g.ResponseWriter.Write(p)
	}
	g.buf = append(g.buf, p...)
	if len(g.buf) >= gzMinSize {
		g.decide()
	}
	return len(p), nil
}

// decide решает, сжимать ли ответ, и отправляет накопленное начало.
func (g *gzWriter) decide() {
	g.decided = true
	h := g.Header()
	ct := h.Get("Content-Type")
	if ct == "" && len(g.buf) > 0 {
		ct = http.DetectContentType(g.buf)
		h.Set("Content-Type", ct)
	}
	ok := len(g.buf) >= gzMinSize && h.Get("Content-Encoding") == "" &&
		g.status != http.StatusNoContent && g.status != http.StatusNotModified && g.status != http.StatusPartialContent
	if ok {
		ok = false
		for _, c := range compressible {
			if strings.HasPrefix(ct, c) {
				ok = true
				break
			}
		}
	}
	h.Add("Vary", "Accept-Encoding")
	if ok {
		h.Set("Content-Encoding", "gzip")
		h.Del("Content-Length")
		g.gz = gzPool.Get().(*gzip.Writer)
		g.gz.Reset(g.ResponseWriter)
	}
	g.ResponseWriter.WriteHeader(g.status)
	if len(g.buf) > 0 {
		if g.gz != nil {
			g.gz.Write(g.buf)
		} else {
			g.ResponseWriter.Write(g.buf)
		}
	}
	g.buf = nil
}

func (g *gzWriter) close() {
	if g.status == 0 {
		return // ответ так и не начали: ничего не пишем, net/http сам ответит 200
	}
	if !g.decided {
		g.decide()
	}
	if g.gz != nil {
		g.gz.Close()
		gzPool.Put(g.gz)
		g.gz = nil
	}
}

// Flush нужен потоковым ответам: отдаём то, что уже есть.
func (g *gzWriter) Flush() {
	if !g.decided && g.status != 0 {
		g.decide()
	}
	if g.gz != nil {
		g.gz.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (g *gzWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }
