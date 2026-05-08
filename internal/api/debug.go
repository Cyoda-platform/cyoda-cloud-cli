package api

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
)

// EnvDebug is the environment variable that, when set to a truthy value
// (anything non-empty other than "0", "false", "no"), enables HTTP debug
// logging via WrapDebug. Documented for users so it can be flipped without
// reading source.
const EnvDebug = "CYODA_CLOUD_DEBUG"

// debugTransport wraps an underlying RoundTripper, writing a trace of each
// request and response to out. Authorization and Cookie headers are redacted
// to avoid leaking secrets to journald / shell history. Bodies are inlined
// when they fit; oversized bodies are truncated with an explicit marker.
//
// debugTransport sits OUTSIDE the auth-injecting Transport in the chain, so
// what it logs is exactly what the auth layer ultimately sends after token
// refresh and 401-retry. The Authorization header therefore appears for
// every request — but always redacted, to keep the trace shareable.
type debugTransport struct {
	inner http.RoundTripper
	out   io.Writer
	// maxBodyBytes caps how much body we inline. Larger bodies are written
	// up to the cap with a "[truncated, N bytes total]" suffix.
	maxBodyBytes int
}

// WrapDebug wraps inner with a stderr-logging RoundTripper iff the
// CYODA_CLOUD_DEBUG env var (see EnvDebug) is set to a truthy value. When
// unset, returns inner unchanged — zero overhead in the production path.
//
// out receives the trace; passing nil routes to os.Stderr is the caller's
// responsibility (we keep WrapDebug pure to make tests trivial).
func WrapDebug(inner http.RoundTripper, out io.Writer, enabled bool) http.RoundTripper {
	if !enabled {
		return inner
	}
	return &debugTransport{
		inner:        inner,
		out:          out,
		maxBodyBytes: 8 << 10, // 8 KiB — generous for problem+json, caps log spam.
	}
}

func (d *debugTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	d.logRequest(req)
	resp, err := d.inner.RoundTrip(req)
	if err != nil {
		fmt.Fprintf(d.out, "← error: %v\n\n", err)
		return nil, err
	}
	d.logResponse(resp)
	return resp, nil
}

func (d *debugTransport) logRequest(req *http.Request) {
	fmt.Fprintf(d.out, "→ %s %s\n", req.Method, req.URL.Redacted())
	d.logHeaders(req.Header)
	if req.Body != nil && req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			fmt.Fprintf(d.out, "  body: <getbody error: %v>\n", err)
			return
		}
		d.logBody(body, req.ContentLength)
	}
}

func (d *debugTransport) logResponse(resp *http.Response) {
	fmt.Fprintf(d.out, "← %d %s\n", resp.StatusCode, resp.Status)
	d.logHeaders(resp.Header)
	// Reading resp.Body consumes it; we tee back into a fresh ReadCloser so
	// the caller's decoder still works.
	if resp.Body != nil {
		buf, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			fmt.Fprintf(d.out, "  body: <read error: %v>\n", err)
			resp.Body = io.NopCloser(bytes.NewReader(nil))
			return
		}
		d.writeBodyBytes(buf, int64(len(buf)))
		resp.Body = io.NopCloser(bytes.NewReader(buf))
	}
	fmt.Fprintln(d.out)
}

func (d *debugTransport) logHeaders(h http.Header) {
	for k, vs := range h {
		switch http.CanonicalHeaderKey(k) {
		case "Authorization", "Cookie", "Set-Cookie", "Proxy-Authorization":
			for range vs {
				fmt.Fprintf(d.out, "  %s: <redacted>\n", k)
			}
		default:
			for _, v := range vs {
				fmt.Fprintf(d.out, "  %s: %s\n", k, v)
			}
		}
	}
}

func (d *debugTransport) logBody(r io.ReadCloser, contentLength int64) {
	defer r.Close()
	buf, err := io.ReadAll(r)
	if err != nil {
		fmt.Fprintf(d.out, "  body: <read error: %v>\n", err)
		return
	}
	d.writeBodyBytes(buf, contentLength)
}

func (d *debugTransport) writeBodyBytes(buf []byte, contentLength int64) {
	if len(buf) == 0 {
		return
	}
	if d.maxBodyBytes > 0 && len(buf) > d.maxBodyBytes {
		fmt.Fprintf(d.out, "  body: %s [truncated, %d bytes total]\n", buf[:d.maxBodyBytes], contentLength)
		return
	}
	fmt.Fprintf(d.out, "  body: %s\n", buf)
}

// IsDebugEnabled reports whether v is a truthy value for EnvDebug. Centralised
// so the parsing rule lives in one place: any non-empty string except a small
// "off" allowlist enables debug.
func IsDebugEnabled(v string) bool {
	switch v {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}
