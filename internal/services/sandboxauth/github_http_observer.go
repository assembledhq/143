package sandboxauth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

type GitHubHTTPObserver struct {
	client   *Client
	stderr   io.Writer
	mu       sync.Mutex
	response *Response
	base     http.RoundTripper
}

func NewGitHubHTTPObserver(socketPath string, stderr io.Writer) *GitHubHTTPObserver {
	if stderr == nil {
		stderr = io.Discard
	}
	return &GitHubHTTPObserver{client: NewClient(socketPath), stderr: stderr, base: http.DefaultTransport}
}

func (o *GitHubHTTPObserver) GetAPIToken(ctx context.Context) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.response != nil {
		return o.response.Token, nil
	}
	response, err := o.client.GetObservedAPIToken(ctx, "sandbox_tools")
	if err != nil {
		return "", err
	}
	if response.Error != "" || response.Token == "" {
		return "", errors.New("sandbox GitHub credential unavailable")
	}
	o.response = response
	return response.Token, nil
}

func (o *GitHubHTTPObserver) HTTPClient() *http.Client {
	return &http.Client{Transport: o, Timeout: 30 * time.Second}
}

func (o *GitHubHTTPObserver) RoundTrip(request *http.Request) (*http.Response, error) {
	o.mu.Lock()
	binding := o.response
	o.mu.Unlock()
	if binding == nil || binding.ObservationID == "" || request.URL.Host != "api.github.com" || request.URL.Scheme != "https" {
		return o.base.RoundTrip(request)
	}
	auth := request.Header.Get("Authorization")
	unknown := subtle.ConstantTimeCompare([]byte(auth), []byte("token "+binding.Token)) != 1 && subtle.ConstantTimeCompare([]byte(auth), []byte("Bearer "+binding.Token)) != 1
	request = request.WithContext(context.WithValue(request.Context(), unknownGHPrincipal{}, unknown))
	return (ghObservationTransport{base: o.base, observe: func(event HTTPObservation) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(request.Context()), time.Second)
		defer cancel()
		if err := o.client.Observe(ctx, binding.ObservationID, []HTTPObservation{event}, false); err != nil {
			fmt.Fprintln(o.stderr, "143-tools github: GitHub request attribution incomplete")
		}
	}}).RoundTrip(request)
}
