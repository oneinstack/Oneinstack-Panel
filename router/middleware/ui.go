package middleware

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"errors"
	"net/http"
	"oneinstack/comm"
	"oneinstack/utils/httpex"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

func MidUiHandle(c *gin.Context) {
	c.Next()
	if c.Writer.Status() != http.StatusNotFound || c.Writer.Size() > 0 {
		return
	}
	pth := c.Request.URL.Path
	r, err := getFile(pth[1:])
	if err != nil {
		r, err = getFile("index.html")
	}
	if err != nil {
		//c.String(404, "rdr err:"+err.Error())
		httpex.ResMsgUrl(c, "未找到内容,跳转中...", "/")
		return
	}
	rd, err := r.Open()
	if err != nil {
		//c.String(500, "open err:"+err.Error())
		httpex.ResMsgUrl(c, "内容有误,跳转中...", "/")
		return
	}
	defer rd.Close()
	c.Writer.Header().Set("Cache-Control", "max-age=360000000")

	ext := filepath.Ext(r.Name)
	if ext == ".html" {
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("Pragma", "no-cache")
		c.Writer.Header().Set("Expires", "0")
		c.Writer.Header().Set("Content-Type", "text/html")
	} else if ext == ".css" {
		c.Writer.Header().Set("Content-Type", "text/css")
	} else if ext == ".js" {
		c.Writer.Header().Set("Content-Type", "application/javascript")
	} else if ext == ".svg" {
		c.Writer.Header().Set("Content-Type", "image/svg+xml")
	} else if ext == ".woff2" {
		//c.Writer.Header().Set("Content-Type", "image/svg+xml")
	} else if ext == ".ttf" || ext == ".ttc" {
		c.Writer.Header().Set("Content-Type", "application/x-font-ttf")
	}
	c.Status(200)
	bts := make([]byte, 1024)
	for !httpex.EndContext(c) {
		n, err := rd.Read(bts)
		if n <= 0 {
			break
		}
		c.Writer.Write(bts[:n])
		if err != nil {
			break
		}
	}
}

var rder *zip.Reader

func getRdr() (*zip.Reader, error) {
	if rder != nil {
		return rder, nil
	}
	bts, err := base64.StdEncoding.DecodeString(comm.StaticPkg)
	if err != nil {
		return nil, err
	}
	buf := bytes.NewReader(bts)
	r, err := zip.NewReader(buf, buf.Size())
	if err != nil {
		return nil, err
	}
	rder = r
	return rder, nil
}
func getFile(pth string) (*zip.File, error) {
	if pth == "" {
		return nil, errors.New("param err")
	}
	//println("getFile:" + pth)
	r, err := getRdr()
	if err != nil {
		return nil, err
	}
	for _, f := range r.File {
		nm := strings.ReplaceAll(f.Name, "\\", "/")
		//println(fmt.Sprintf("find zip file:%s, %s",pth, nm))
		if pth == nm {
			return f, nil
		}
	}
	return nil, errors.New("file not found")
}
