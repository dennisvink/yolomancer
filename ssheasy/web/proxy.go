package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/net/publicsuffix"

	"syscall/js"
)

var (
	proxyClient *http.Client
)

func startProxy() {
	if sshClient == nil {
		log.Print("proxy: no ssh connection available")
		return
	}
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		log.Printf("failed to create cookiejar: %v", err)
		return
	}
	proxyClient = &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				log.Printf("proxy dial: %s", addr)
				addr = tryResolveRemote(sshClient, addr)
				log.Printf(" resolved addr: %s", addr)
				a, err := net.ResolveTCPAddr("tcp", addr)
				if err != nil {
					log.Printf("proxy: failed to resolve addr %s: %v", addr, err)
					return nil, err
				}
				c, err := sshClient.DialTCP("tcp", nil, a)
				if err != nil {
					log.Printf("proxy: failed to connect to remote addr %v", err)
				}
				return c, err
			},
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Jar: jar,
	}
}

// tryResolveRemote tries to resolve the hostname on the ssh server calling `getent hosst addr`
// if there's any error it returns the original addr
func tryResolveRemote(c *ssh.Client, addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		log.Printf("failed to split host: %v", err)
		return addr
	}
	// resolve on the remote side
	session, err := sshClient.NewSession()
	if err != nil {
		log.Printf("failed to create session: %v", err)
		return addr
	}
	defer session.Close()

	var b bytes.Buffer
	session.Stdout = &b
	err = session.Run(fmt.Sprintf("getent ahosts %s", host))
	if err != nil {
		log.Printf("failed to run resolve cmd: %v", err)
		return addr
	}
	// output will show: "IP_ADDRESS hostname"
	out := b.String()
	log.Printf("resolved addrs: %s", out)
	if out == "" {
		return addr
	}
	ipaddr := strings.Split(out, " ")[0]
	if strings.Contains(ipaddr, "::") {
		return fmt.Sprintf("[%s]:%s", ipaddr, port)

	}
	return ipaddr + ":" + port
}

func forward(this js.Value, args []js.Value) interface{} {
	if len(args) < 2 {
		log.Print("proxy: too few arguments")
		return nil
	}
	if proxyClient == nil {
		startProxy()
	}
	go func() {
		req := args[0]
		cb := args[1]
		id := req.Get("id").String()
		ssheasyHost := req.Get("ssheasyHost").String()
		sshClientID := req.Get("sshClientID").String()
		m := req.Get("method").String()
		u := req.Get("url").String()
		explicitURL := false
		if strings.Contains(u, "/portforward/") {
			u = strings.Split(u, "/portforward/")[1]
			u = strings.SplitN(u, "/", 2)[1]
			explicitURL = true
		}

		pu, err := url.Parse(u)
		if err != nil {
			log.Printf("proxy: failed to parse url: %v", err)
			cb.Invoke(errResp(id, err))
			return
		}
		log.Printf("path: %s", pu.Path)
		if strings.Contains(pu.Path, "//") {
			pu.Path = strings.ReplaceAll(pu.Path, "//", "/")
		}
		if !explicitURL && pu.Host == ssheasyHost {

			host := req.Get("host").String()
			if host != "" {
				rh, err := url.Parse(host)
				if err != nil {
					log.Printf("proxy: failed to parse host: %v", err)
				}
				rh.Path = pu.Path
				rh.RawQuery = pu.RawQuery
				log.Printf("url is rewriten from %s to %s", pu.String(), rh.String())
				pu = rh
			}
		}

		u = pu.String()
		h := req.Get("headers")
		b := req.Get("body")
		body := new(bytes.Buffer)
		if bl := b.Get("length").Int(); bl > 0 {
			bb := make([]byte, bl)
			js.CopyBytesToGo(bb, b)
			body = bytes.NewBuffer(bb)
		}
		r, err := http.NewRequest(m, u, body)
		if err != nil {
			log.Printf("proxy: failed to create request: %v", err)
			cb.Invoke(errResp(id, err))
			return
		}
		for i := 0; i < h.Length(); i++ {
			e := h.Index(i)
			r.Header[e.Index(0).String()] = []string{e.Index(1).String()}
		}
		// dbg, err := httputil.DumpRequestOut(r, true)
		// if err != nil {
		// 	log.Printf("proxy: failed to dump request: %v", err)
		// }
		// log.Printf("request sent with id %s: [%s]", id, string(dbg))
		resp, err := proxyClient.Do(r)
		if err != nil {
			log.Printf("proxy: failed to do request: %v", err)
			cb.Invoke(errResp(id, err))
			return
		}
		defer resp.Body.Close()
		// dbg, err = httputil.DumpResponse(resp, true)
		// if err != nil {
		// 	log.Printf("proxy: failed to dump response: %v", err)
		// }
		// log.Printf("response received with id %s: [%s]", id, string(dbg))
		rb, err := io.ReadAll(resp.Body)
		if err != nil {
			log.Printf("proxy: failed to read resp: %v", err)
			cb.Invoke(errResp(id, err))
			return
		}
		var headers []interface{}
		for hk, hv := range resp.Header {
			if strings.EqualFold(hk, "Location") && (strings.HasPrefix(hv[0], "http://") || strings.HasPrefix(hv[0], "https://")) {
				hv[0] = fmt.Sprintf("/portforward/%s/%s", sshClientID, hv[0])
			}
			headers = append(headers, []interface{}{hk, hv[0]})
		}

		buf := uint8Array.New(len(rb))
		js.CopyBytesToJS(buf, rb)

		log.Printf("proxy call finished with status code: %d", resp.StatusCode)
		cb.Invoke(map[string]interface{}{
			"id":        id,
			"body":      buf,
			"headers":   headers,
			"status":    resp.StatusCode,
			"resp_text": resp.Status,
		})
	}()
	return nil
}

func errResp(id string, err error) map[string]interface{} {
	return map[string]interface{}{
		"id":    id,
		"error": err.Error(),
	}
}
