package media

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

const downloadTestURL = "https://download.example/media?token=secret-download-token"

type downloadTestTransport struct {
	roundTrip func(*http.Request) (*http.Response, error)
	closed    bool
	calls     int
}

func (tr *downloadTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tr.calls++
	return tr.roundTrip(req)
}

func (tr *downloadTestTransport) CloseIdleConnections() { tr.closed = true }

type downloadTestBody struct {
	io.Reader
	closed bool
}

func (b *downloadTestBody) Close() error { b.closed = true; return nil }

func downloadPublicResolver(_ context.Context, network, host string) ([]net.IP, error) {
	return []net.IP{net.ParseIP("8.8.8.8")}, nil
}

func downloadTestFactory(tr *downloadTestTransport) func([]net.IP) downloadTransport {
	return func([]net.IP) downloadTransport { return tr }
}

func TestSafeDownloadSizeLimit(t *testing.T) {
	for _, tc := range []struct {
		name          string
		contentLength int64
		body          string
		tooLarge      bool
	}{
		{"known oversized", 5, "12345", true},
		{"unknown oversized", -1, "12345", true},
		{"unknown much larger", -1, "1234567890", true},
		{"underreported length", 2, "12345", true},
		{"known exact boundary", 4, "1234", false},
		{"unknown exact boundary", -1, "1234", false},
		{"empty", 0, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := strings.NewReader(tc.body)
			body := &downloadTestBody{Reader: reader}
			tr := &downloadTestTransport{roundTrip: func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, ContentLength: tc.contentLength, Body: body}, nil
			}}
			data, err := safeDownload(context.Background(), downloadTestURL, 4, downloadPublicResolver, downloadTestFactory(tr))
			if tc.tooLarge {
				if !errors.Is(err, ErrTooLarge) || data != nil {
					t.Errorf("oversized download = %q, %v; want nil, ErrTooLarge", data, err)
				}
			} else if err != nil || string(data) != tc.body {
				t.Errorf("download = %q, %v; want %q, nil", data, err, tc.body)
			}
			read := len(tc.body) - reader.Len()
			if read > 5 || (tc.contentLength > 4 && read != 0) {
				t.Errorf("read %d bytes with maxBytes=4 and ContentLength=%d", read, tc.contentLength)
			}
			if !body.closed || !tr.closed {
				t.Errorf("resources not closed: body=%v, idle connections=%v", body.closed, tr.closed)
			}
		})
	}
}

type downloadErrorReader struct {
	err error
}

func (r downloadErrorReader) Read(p []byte) (int, error) {
	return copy(p, "partial"), r.err
}

func TestSafeDownloadStreamError(t *testing.T) {
	body := &downloadTestBody{Reader: downloadErrorReader{err: errors.New("stream failed for " + downloadTestURL)}}
	tr := &downloadTestTransport{roundTrip: func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, ContentLength: -1, Body: body}, nil
	}}
	data, err := safeDownload(context.Background(), downloadTestURL, 20, downloadPublicResolver, downloadTestFactory(tr))
	if data != nil || err == nil || err.Error() != "media: download failed" {
		t.Errorf("stream error = %q, %v; want nil, fixed safe error", data, err)
	}
	if !body.closed || !tr.closed {
		t.Errorf("resources not closed: body=%v, idle connections=%v", body.closed, tr.closed)
	}
}

func TestSafeDownloadRequestErrorIsSafe(t *testing.T) {
	tr := &downloadTestTransport{roundTrip: func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connect failed for " + downloadTestURL)
	}}
	data, err := safeDownload(context.Background(), downloadTestURL, 20, downloadPublicResolver, downloadTestFactory(tr))
	if data != nil || err == nil || err.Error() != "media: download failed" {
		t.Errorf("request error = %q, %v; want nil, fixed safe error", data, err)
	}
	if !tr.closed {
		t.Error("idle connections not closed after request failure")
	}
}

func TestSafeDownloadPrivateURLs(t *testing.T) {
	for _, raw := range []string{
		"https://127.0.0.1/file?token=secret",
		"https://10.0.0.1/file",
		"https://172.16.0.1/file",
		"https://192.168.0.1/file",
		"https://169.254.169.254/file",
		"https://[::1]/file",
		"https://[fc00::1]/file",
		"https://[::ffff:127.0.0.1]/file",
	} {
		t.Run(raw, func(t *testing.T) {
			data, err := SafeDownload(context.Background(), raw, 4)
			if data != nil || !errors.Is(err, ErrUnsafeURL) {
				t.Errorf("private URL = %q, %v; want nil, ErrUnsafeURL", data, err)
			}
		})
	}
}

func TestSafeDownloadRejectsNonPublicDNS(t *testing.T) {
	for _, addr := range []string{
		"127.0.0.1", "10.0.0.1", "172.31.255.255", "192.168.0.1", "169.254.169.254",
		"0.0.0.0", "0.1.2.3", "224.0.0.1", "240.0.0.1", "255.255.255.255",
		"100.64.0.1", "100.127.255.254", "::", "::1", "fc00::1", "fe80::1", "ff02::1", "::ffff:100.64.0.1",
	} {
		t.Run(addr, func(t *testing.T) {
			resolve := func(context.Context, string, string) ([]net.IP, error) {
				// A public answer must not hide a prohibited answer in the same set.
				return []net.IP{net.ParseIP("8.8.8.8"), net.ParseIP(addr)}, nil
			}
			created := false
			tr := &downloadTestTransport{roundTrip: func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, ContentLength: 0, Body: io.NopCloser(strings.NewReader(""))}, nil
			}}
			data, err := safeDownload(context.Background(), downloadTestURL, 4, resolve, func([]net.IP) downloadTransport {
				created = true
				return tr
			})
			if data != nil || !errors.Is(err, ErrUnsafeURL) || created {
				t.Errorf("nonpublic DNS = %q, %v, transport created=%v; want nil, ErrUnsafeURL, false", data, err, created)
			}
		})
	}
}

func TestSafeDownloadInvalidURL(t *testing.T) {
	for _, raw := range []string{"http://download.example/file", "https://user:token@download.example/file", "https:///file", "https://download.example/%zz?token=secret", "https://download.example:bad/file"} {
		t.Run(raw, func(t *testing.T) {
			called := false
			resolve := func(context.Context, string, string) ([]net.IP, error) {
				called = true
				return nil, errors.New("unexpected resolution")
			}
			data, err := safeDownload(context.Background(), raw, 4, resolve, nil)
			if data != nil || !errors.Is(err, ErrUnsafeURL) || called {
				t.Errorf("invalid URL = %q, %v, resolved=%v; want nil, ErrUnsafeURL, false", data, err, called)
			}
		})
	}
}

func TestSafeDownloadDNSFailureIsSafe(t *testing.T) {
	resolve := func(context.Context, string, string) ([]net.IP, error) {
		return nil, errors.New("DNS failed for " + downloadTestURL)
	}
	data, err := safeDownload(context.Background(), downloadTestURL, 4, resolve, nil)
	if data != nil || !errors.Is(err, ErrUnsafeURL) {
		t.Errorf("DNS failure = %q, %v; want nil, ErrUnsafeURL", data, err)
	}
}

func TestSafeDownloadRedirect(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			body := &downloadTestBody{Reader: strings.NewReader("redirect response")}
			tr := &downloadTestTransport{roundTrip: func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: status, Header: http.Header{"Location": {"https://127.0.0.1/private?token=secret"}}, Body: body}, nil
			}}
			data, err := safeDownload(context.Background(), downloadTestURL, 40, downloadPublicResolver, downloadTestFactory(tr))
			if data != nil || err == nil || tr.calls != 1 {
				t.Errorf("redirect = %q, %v, calls=%d; want nil, error, one request", data, err, tr.calls)
			}
			if err != nil && strings.Contains(err.Error(), "token") {
				t.Errorf("redirect error leaks URL: %v", err)
			}
			if !body.closed || !tr.closed {
				t.Errorf("resources not closed: body=%v, idle connections=%v", body.closed, tr.closed)
			}
		})
	}
}

func TestSafeDownloadContextDuringDNS(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resolve := func(ctx context.Context, _, _ string) ([]net.IP, error) {
		cancel()
		<-ctx.Done()
		return nil, ctx.Err()
	}
	data, err := safeDownload(ctx, downloadTestURL, 4, resolve, nil)
	if data != nil || !errors.Is(err, context.Canceled) || err.Error() != context.Canceled.Error() {
		t.Errorf("DNS cancellation = %q, %v; want nil, context.Canceled", data, err)
	}
}

func TestSafeDownloadContextDuringRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tr := &downloadTestTransport{roundTrip: func(req *http.Request) (*http.Response, error) {
		cancel()
		<-req.Context().Done()
		return nil, req.Context().Err()
	}}
	data, err := safeDownload(ctx, downloadTestURL, 4, downloadPublicResolver, downloadTestFactory(tr))
	if data != nil || !errors.Is(err, context.Canceled) || err.Error() != context.Canceled.Error() {
		t.Errorf("request cancellation = %q, %v; want nil, safe context.Canceled", data, err)
	}
	if !tr.closed {
		t.Error("idle connections not closed after request cancellation")
	}
}

func TestSafeDownloadTotalDeadline(t *testing.T) {
	start := time.Now()
	var dnsDeadline time.Time
	var dnsContext context.Context
	resolve := func(ctx context.Context, network, host string) ([]net.IP, error) {
		dnsContext = ctx
		var ok bool
		dnsDeadline, ok = ctx.Deadline()
		if !ok || dnsDeadline.After(time.Now().Add(60*time.Second)) || dnsDeadline.Before(start.Add(59*time.Second)) {
			t.Errorf("DNS does not share a maximum 60s total deadline: %v, present=%v", dnsDeadline, ok)
		}
		return downloadPublicResolver(ctx, network, host)
	}
	tr := &downloadTestTransport{roundTrip: func(req *http.Request) (*http.Response, error) {
		deadline, ok := req.Context().Deadline()
		if !ok || !deadline.Equal(dnsDeadline) {
			t.Errorf("request deadline %v differs from DNS deadline %v", deadline, dnsDeadline)
		}
		return &http.Response{StatusCode: http.StatusOK, ContentLength: 0, Body: io.NopCloser(strings.NewReader(""))}, nil
	}}
	_, err := safeDownload(context.Background(), downloadTestURL, 4, resolve, downloadTestFactory(tr))
	if err != nil {
		t.Fatal(err)
	}
	if dnsContext.Err() != context.Canceled {
		t.Errorf("download context remains live after return: %v", dnsContext.Err())
	}
}

type downloadCancelReader struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func (r downloadCancelReader) Read(p []byte) (int, error) {
	r.cancel()
	<-r.ctx.Done()
	return copy(p, "partial"), r.ctx.Err()
}

func TestSafeDownloadContextDuringBody(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var body *downloadTestBody
	tr := &downloadTestTransport{roundTrip: func(req *http.Request) (*http.Response, error) {
		body = &downloadTestBody{Reader: downloadCancelReader{ctx: req.Context(), cancel: cancel}}
		return &http.Response{StatusCode: http.StatusOK, ContentLength: -1, Body: body}, nil
	}}
	data, err := safeDownload(ctx, downloadTestURL, 20, downloadPublicResolver, downloadTestFactory(tr))
	if data != nil || !errors.Is(err, context.Canceled) || err.Error() != context.Canceled.Error() {
		t.Errorf("body cancellation = %q, %v; want nil, safe context.Canceled", data, err)
	}
	if !body.closed || !tr.closed {
		t.Errorf("resources not closed: body=%v, idle connections=%v", body.closed, tr.closed)
	}
}

func TestSafeDownloadAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	data, err := safeDownload(ctx, downloadTestURL, 20, nil, nil)
	if data != nil || !errors.Is(err, context.Canceled) {
		t.Errorf("already canceled = %q, %v; want nil, context.Canceled before DNS", data, err)
	}
}

func TestSafeDownloadCallerDeadline(t *testing.T) {
	deadline := time.Now().Add(time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	resolve := func(ctx context.Context, network, host string) ([]net.IP, error) {
		if got, ok := ctx.Deadline(); !ok || !got.Equal(deadline) {
			t.Errorf("DNS deadline = %v, present=%v; want caller deadline %v", got, ok, deadline)
		}
		return downloadPublicResolver(ctx, network, host)
	}
	tr := &downloadTestTransport{roundTrip: func(req *http.Request) (*http.Response, error) {
		if got, ok := req.Context().Deadline(); !ok || !got.Equal(deadline) {
			t.Errorf("request deadline = %v, present=%v; want caller deadline %v", got, ok, deadline)
		}
		return &http.Response{StatusCode: http.StatusOK, ContentLength: 0, Body: io.NopCloser(strings.NewReader(""))}, nil
	}}
	if _, err := safeDownload(ctx, downloadTestURL, 20, resolve, downloadTestFactory(tr)); err != nil {
		t.Fatal(err)
	}
}

func TestSafeDownloadDNSDeadlineExpires(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	resolve := func(ctx context.Context, _, _ string) ([]net.IP, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	data, err := safeDownload(ctx, downloadTestURL, 20, resolve, nil)
	if data != nil || !errors.Is(err, context.DeadlineExceeded) || err.Error() != context.DeadlineExceeded.Error() {
		t.Errorf("DNS deadline exceeded = %q, %v; want nil, safe context.DeadlineExceeded", data, err)
	}
}

func TestSafeDownloadResolvesOnceAndPreservesHost(t *testing.T) {
	calls := 0
	resolve := func(ctx context.Context, network, host string) ([]net.IP, error) {
		calls++
		if network != "ip" || host != "download.example" {
			t.Errorf("DNS query = %q, %q", network, host)
		}
		return []net.IP{net.ParseIP("8.8.8.8"), net.ParseIP("2606:4700:4700::1111")}, nil
	}
	tr := &downloadTestTransport{roundTrip: func(req *http.Request) (*http.Response, error) {
		if req.Host != "download.example:8443" || req.URL.Host != req.Host || req.URL.Scheme != "https" {
			t.Errorf("original TLS/HTTP host was changed: %v", req.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, ContentLength: 0, Body: io.NopCloser(strings.NewReader(""))}, nil
	}}
	factory := func(ips []net.IP) downloadTransport {
		if len(ips) != 2 || !ips[0].Equal(net.ParseIP("8.8.8.8")) || !ips[1].Equal(net.ParseIP("2606:4700:4700::1111")) {
			t.Errorf("transport did not receive validated answers: %v", ips)
		}
		return tr
	}
	_, err := safeDownload(context.Background(), "https://download.example:8443/media?token=secret", 20, resolve, factory)
	if err != nil || calls != 1 {
		t.Errorf("download = %v, DNS calls=%d; want nil, 1", err, calls)
	}
}

func TestDownloadDialPinsValidatedIPs(t *testing.T) {
	ips := []net.IP{net.ParseIP("8.8.8.8"), net.ParseIP("2606:4700:4700::1111")}
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var addresses []string
	dial := downloadDialContext(ips, func(gotCtx context.Context, network, address string) (net.Conn, error) {
		if gotCtx != ctx || network != "tcp" {
			t.Errorf("dial did not preserve context/network: %v, %q", gotCtx, network)
		}
		addresses = append(addresses, address)
		if len(addresses) == 1 {
			return nil, errors.New("first address unreachable")
		}
		return client, nil
	})
	// Mutating the resolver result must not change the pinned addresses.
	ips[0][len(ips[0])-1] = 9
	ips[1] = net.ParseIP("::1")
	conn, err := dial(ctx, "tcp", "download.example:8443")
	if err != nil || conn != client {
		t.Fatalf("dial = %v, %v; want successful second address", conn, err)
	}
	if got := strings.Join(addresses, ","); got != "8.8.8.8:8443,[2606:4700:4700::1111]:8443" {
		t.Errorf("dialed %q; want only validated original IP addresses", got)
	}
}

func TestDownloadDialStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	dial := downloadDialContext([]net.IP{net.ParseIP("8.8.8.8"), net.ParseIP("1.1.1.1")}, func(context.Context, string, string) (net.Conn, error) {
		calls++
		cancel()
		return nil, context.Canceled
	})
	conn, err := dial(ctx, "tcp", "download.example:443")
	if conn != nil || !errors.Is(err, context.Canceled) || calls != 1 {
		t.Errorf("dial after cancellation = %v, %v, calls=%d", conn, err, calls)
	}
}

func TestDownloadTransportDoesNotInheritProxy(t *testing.T) {
	tr := newDownloadTransport([]net.IP{net.ParseIP("8.8.8.8")}).(*http.Transport)
	defer tr.CloseIdleConnections()
	if tr.Proxy != nil {
		t.Error("download transport inherits an environment proxy callback")
	}
}
