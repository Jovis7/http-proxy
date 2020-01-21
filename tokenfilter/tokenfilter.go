package tokenfilter

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"strings"

	"github.com/getlantern/golog"
	"github.com/getlantern/ops"
	"github.com/getlantern/proxy/filters"

	"github.com/getlantern/http-proxy-lantern/common"
	"github.com/getlantern/http-proxy-lantern/instrument"
	"github.com/getlantern/http-proxy-lantern/mimic"
)

var log = golog.LoggerFor("tokenfilter")

type tokenFilter struct {
	token      string
	instrument instrument.Instrument
}

func New(token string, instrument instrument.Instrument) filters.Filter {
	return &tokenFilter{
		token:      token,
		instrument: instrument,
	}
}

func (f *tokenFilter) Apply(ctx filters.Context, req *http.Request, next filters.Next) (*http.Response, filters.Context, error) {
	op := ops.Begin("tokenfilter")
	defer op.End()

	if log.IsTraceEnabled() {
		reqStr, _ := httputil.DumpRequest(req, true)
		log.Tracef("Token Filter Middleware received request:\n%s", reqStr)
	}

	if f.token == "" {
		log.Trace("Not checking token")
		return next(ctx, req)
	}

	tokens := req.Header[common.TokenHeader]
	if tokens == nil || len(tokens) == 0 || tokens[0] == "" {
		log.Error(errorf(op, "No token provided, mimicking apache"))
		f.instrument.Mimic(true)
		return mimicApache(ctx, req)
	}
	tokenMatched := false
	for _, candidate := range tokens {
		if candidate == f.token {
			tokenMatched = true
			break
		}
	}
	if tokenMatched {
		req.Header.Del(common.TokenHeader)
		log.Tracef("Allowing connection from %v to %v", req.RemoteAddr, req.Host)
		f.instrument.Mimic(false)
		return next(ctx, req)
	}
	log.Error(errorf(op, "Mismatched token(s) %v, mimicking apache", strings.Join(tokens, ",")))
	f.instrument.Mimic(true)
	return mimicApache(ctx, req)
}

func errorf(op ops.Op, msg string, args ...interface{}) error {
	return op.FailIf(fmt.Errorf(msg, args...))
}

func mimicApache(ctx filters.Context, req *http.Request) (*http.Response, filters.Context, error) {
	conn := ctx.DownstreamConn()
	mimic.Apache(conn, req)
	conn.Close()
	return nil, ctx, nil
}
