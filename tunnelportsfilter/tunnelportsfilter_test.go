package tunnelportsfilter

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/getlantern/testify/assert"
)

func TestFilterTunnelPorts(t *testing.T) {
	filter, _ := New(http.NotFoundHandler(), AllowedPorts([]int{443, 8080}))
	server := httptest.NewServer(filter)
	defer server.Close()
	u, _ := url.Parse(server.URL)
	client := http.Client{Transport: &http.Transport{
		Dial: func(network, addr string) (net.Conn, error) {
			return net.Dial("tcp", u.Host)
		},
	}}

	req, _ := http.NewRequest("CONNECT", "http://site.com", nil)
	resp, _ := client.Do(req)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "")

	req, _ = http.NewRequest("CONNECT", "http://site.com:", nil)
	resp, _ = client.Do(req)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "")

	req, _ = http.NewRequest("CONNECT", "http://site.com:abc", nil)
	resp, _ = client.Do(req)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "")

	req, _ = http.NewRequest("CONNECT", "http://site.com:443", nil)
	resp, _ = client.Do(req)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "")

	req, _ = http.NewRequest("CONNECT", "http://site.com:8080", nil)
	resp, _ = client.Do(req)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "")

	req, _ = http.NewRequest("CONNECT", "http://site.com:8081", nil)
	resp, _ = client.Do(req)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "")
}
