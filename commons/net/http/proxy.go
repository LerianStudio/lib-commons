package http

import (
	constant "github.com/LerianStudio/lib-commons/v2/commons/constants"
	"net/http"
	"net/http/httputil"
	"net/url"
)

// ServeReverseProxy serves a reverse proxy for a given url.
func ServeReverseProxy(target string, res http.ResponseWriter, req *http.Request) {
	targetURL, err := url.Parse(target)
	if err != nil {
		http.Error(res, err.Error(), http.StatusInternalServerError)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	// Update the headers to allow for SSL redirection
	req.URL.Host = targetURL.Host
	req.URL.Scheme = targetURL.Scheme
	req.Header.Set(constant.HeaderForwardedHost, req.Header.Get(constant.HeaderHost))
	req.Host = targetURL.Host

	proxy.ServeHTTP(res, req)
}
