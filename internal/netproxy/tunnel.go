package netproxy

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// TunnelStatus asks the HTTP proxy at raw for a tunnel to hostport as a
// CLI given it would (CONNECT, with the user and password its address
// holds), and closes the tunnel at once, before anything is sent through
// it: the proxy's status line ("403 Forbidden") and code, 200 when it let
// the tunnel through. Only an http:// proxy is asked; any other, or one
// that gave no answer, is err. For a sign-in that failed as "proxy refused
// the connection", which Claude Code says without the status (T-107).
func TunnelStatus(ctx context.Context, raw, hostport string) (status string, code int, err error) {
	u, err := Parse(raw)
	if err != nil || u.Scheme != "http" || u.Host == "" {
		return "", 0, errors.New("not an http:// proxy")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", proxyAddr(u))
	if err != nil {
		return "", 0, err
	}
	defer c.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: hostport}, Host: hostport, Header: http.Header{}}
	if u.User != nil {
		pw, _ := u.User.Password()
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(u.User.Username()+":"+pw)))
	}
	if err := req.Write(c); err != nil {
		return "", 0, err
	}
	res, err := http.ReadResponse(bufio.NewReader(c), req)
	if err != nil {
		return "", 0, fmt.Errorf("the proxy's answer to CONNECT didn't read: %w", err)
	}
	return strings.TrimSpace(res.Status), res.StatusCode, nil
}
