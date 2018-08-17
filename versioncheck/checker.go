// Package versioncheck checks if the X-Lantern-Version header in the request
// is absent or below than a semantic version, and rewrite/redirect a fraction
// of such requests to a predefined URL.
//
// For CONNECT tunnels, it simply checks the X-Lantern-Version header in the
// CONNECT request, as it's ineffeicient to inspect the tunneled data
// byte-to-byte. It redirects to the predefined URL via HTTP 302 Found. Note -
// this only works for CONNECT requests whose payload isn't encrypted (i.e.
// CONNECT requests from mobile app to port 80).
//
// For GET requests, it checks if the request is come from browser (via
// User-Agent) and expects HTML content, to be more precise. It rewrites the
// request to access the predefined URL directly.
//
// It doesn't check other HTTP methods.
//
// The purpose is to show an upgrade notice to the users with outdated Lantern
// client.
//
package versioncheck

import (
	"bufio"
	"crypto/tls"
	"io"
	"io/ioutil"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/blang/semver"
	"github.com/getlantern/golog"
	"github.com/getlantern/proxy/filters"

	"github.com/getlantern/http-proxy-lantern/common"
)

var (
	log = golog.LoggerFor("versioncheck")

	random = rand.New(rand.NewSource(time.Now().UnixNano()))
)

const (
	oneMillion = 100 * 100 * 100
)

type VersionChecker struct {
	versionRange     semver.Range
	rewriteURL       *url.URL
	rewriteURLString string
	rewriteAddr      string
	tunnelPorts      []string
	ppm              int
}

// New constructs a VersionChecker to check the request and rewrite/redirect if
// required.  It errors if the versionRange string is not valid, or the rewrite
// URL is malformed. tunnelPortsToCheck defaults to 80 only.
func New(versionRange string, rewriteURL string, tunnelPortsToCheck []string, percentage float64) (*VersionChecker, error) {
	u, err := url.Parse(rewriteURL)
	if err != nil {
		return nil, err
	}
	rewriteAddr := u.Host

	if u.Scheme == "https" {
		rewriteAddr = rewriteAddr + ":443"
	}

	if len(tunnelPortsToCheck) == 0 {
		tunnelPortsToCheck = []string{"80"}
	}
	ver, err := semver.ParseRange(versionRange)
	if err != nil {
		return nil, err
	}
	return &VersionChecker{ver, u, rewriteURL, rewriteAddr, tunnelPortsToCheck, int(percentage * oneMillion)}, nil
}

// Dial is a function that dials a network connection.
type Dial func(network, address string) (net.Conn, error)

// Dialer wraps Dial to dial TLS when the requested host matchs the host in
// rewriteURL. If the rewriteURL is not https, it returns Dial as is.
func (c *VersionChecker) Dialer(d Dial) Dial {
	if c.rewriteURL.Scheme != "https" {
		return d
	}
	return func(network, address string) (net.Conn, error) {
		conn, err := d(network, address)
		if err != nil {
			return conn, err
		}
		if c.rewriteAddr == address {
			conn = tls.Client(conn, &tls.Config{ServerName: c.rewriteURL.Host})
		}
		return conn, err
	}
}

// Filter returns a filters.Filter interface to be used in the filter chain.
func (c *VersionChecker) Filter() filters.Filter {
	return c
}

// Apply satisfies the filters.Filter interface.
func (c *VersionChecker) Apply(ctx filters.Context, req *http.Request, next filters.Next) (*http.Response, filters.Context, error) {
	defer req.Header.Del(common.VersionHeader)
	switch req.Method {
	case http.MethodConnect:
		if c.shouldRedirectOnConnect(req) {
			return c.redirectOnConnect(ctx, req)
		}
	case http.MethodGet:
		// the first request from browser should always be GET
		if c.shouldRedirect(req) {
			return c.redirect(ctx, req)
		}
	}
	return next(ctx, req)
}

func (c *VersionChecker) redirect(ctx filters.Context, req *http.Request) (*http.Response, filters.Context, error) {
	log.Debugf("Redirecting %s %s%s to %s",
		req.Method,
		req.Host,
		req.URL.Path,
		c.rewriteURL.String(),
	)
	return &http.Response{
		StatusCode: http.StatusFound,
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header: http.Header{
			"Location": []string{c.rewriteURLString},
		},
		Close: true,
	}, ctx, nil
}

func (c *VersionChecker) shouldRedirect(req *http.Request) bool {
	// Typical browsers always have this as the first value
	if !strings.HasPrefix(req.Header.Get("Accept"), "text/html") {
		return false
	}
	// This covers almost all browsers
	if !strings.HasPrefix(req.Header.Get("User-Agent"), "Mozilla/") {
		return false
	}
	return c.matchVersion(req)
}

func (c *VersionChecker) shouldRedirectOnConnect(req *http.Request) bool {
	if !c.matchVersion(req) {
		return false
	}
	_, port, err := net.SplitHostPort(req.Host)
	if err != nil {
		return false
	}
	portMeet := false
	for _, p := range c.tunnelPorts {
		if port == p {
			portMeet = true
			break
		}
	}
	return portMeet
}

func (c *VersionChecker) redirectOnConnect(ctx filters.Context, req *http.Request) (*http.Response, filters.Context, error) {
	conn := ctx.DownstreamConn()
	// Acknowledge the CONNECT request
	resp := &http.Response{
		StatusCode: http.StatusOK,
		ProtoMajor: 1,
		ProtoMinor: 1,
	}
	if err := resp.Write(conn); err != nil {
		return nil, ctx, err
	}

	// Consume the first request the application sent over the CONNECT tunnel
	// before sending the response.
	bufReader := bufio.NewReader(conn)
	req, err := http.ReadRequest(bufReader)
	if err != nil {
		log.Errorf("Fail to read tunneled request before redirecting: %v", err)
	}
	if req.Body != nil {
		_, _ = io.Copy(ioutil.Discard, req.Body)
		req.Body.Close()
	}

	// Send the actual response to the application regardless of what the
	// request is, as the request is consumed already.
	return c.redirect(ctx, req)
}

func (c *VersionChecker) matchVersion(req *http.Request) bool {
	// Avoid infinite loop
	if req.Host == c.rewriteURL.Host {
		return false
	}
	version := req.Header.Get(common.VersionHeader)
	v, e := semver.Make(version)
	if e == nil && !c.versionRange(v) {
		return false
	}
	if random.Intn(oneMillion) >= c.ppm {
		return false
	}
	return true
}
