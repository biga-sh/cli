package api

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"
)

const (
	defaultAttempts = 4
	defaultTimeout  = 30 * time.Second
	// Snapshots can be tens of megabytes on a slow uplink.
	uploadTimeout = 5 * time.Minute
	// maxRetryAfter caps how long a server-requested delay is honored.
	maxRetryAfter = 30 * time.Second
	// maxResponseBytes bounds how much of a response body is read.
	maxResponseBytes = 16 << 20
)

// request holds per-call transport settings; zero values select defaults.
type request struct {
	attempts int
	timeout  time.Duration
	// compress allows a gzip body when the server accepts one.
	compress bool
	progress func(sent, total int)
}

// body is an encoded request body.
type body struct {
	data []byte
	gzip bool
}

// do sends a request, retrying network errors, 429s, and 5xx responses with
// jittered exponential backoff, and decodes the response into out. It returns
// the size of the request body as sent.
func (c *Client) do(ctx context.Context, method, route string, in, out any, opts request) (int, error) {
	attempts := cmp.Or(opts.attempts, defaultAttempts)
	timeout := cmp.Or(opts.timeout, defaultTimeout)

	var (
		b body
		// encoded and allowGzip record whether b is ready, and the setting it
		// was encoded under, so it is rebuilt only when that changes.
		encoded, allowGzip bool
	)
	for attempt := 1; ; attempt++ {
		if want := opts.compress && c.gzip.Load(); in != nil && (!encoded || allowGzip != want) {
			var err error
			if b, err = encode(in, want); err != nil {
				return 0, fmt.Errorf("encode request: %w", err)
			}
			encoded, allowGzip = true, want
		}

		data, err := c.attempt(ctx, method, route, b, timeout, opts.progress)
		if err == nil {
			return len(b.data), decode(data, out)
		}
		if b.gzip && HasStatus(err, http.StatusUnsupportedMediaType) {
			// Something between us and the server rejected compression after
			// all; resend plainly without spending an attempt.
			c.gzip.Store(false)
			attempt--
			continue
		}
		if attempt >= attempts || !retryable(ctx, err) {
			return 0, err
		}

		wait := c.backoff(attempt)
		if se, ok := errors.AsType[*StatusError](err); ok && se.RetryAfter > wait {
			wait = min(se.RetryAfter, maxRetryAfter)
		}
		select {
		case <-ctx.Done():
			return 0, err
		case <-time.After(wait):
		}
	}
}

func (c *Client) attempt(ctx context.Context, method, route string, b body, timeout time.Duration, progress func(sent, total int)) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var reader io.Reader
	if b.data != nil {
		reader = bytes.NewReader(b.data)
		if progress != nil {
			reader = &progressReader{r: bytes.NewReader(b.data), total: len(b.data), fn: progress}
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+route, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if b.data != nil {
		req.ContentLength = int64(len(b.data))
		req.Header.Set("Content-Type", "application/json")
		if b.gzip {
			req.Header.Set("Content-Encoding", "gzip")
		}
	}

	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() == nil || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = &UnreachableError{URL: c.baseURL, Err: err}
		}
		return nil, err
	}
	defer resp.Body.Close()

	if acceptsGzip(resp.Header) {
		c.gzip.Store(true)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		se := statusError(resp.StatusCode, data)
		se.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		return nil, se
	}
	return data, nil
}

func acceptsGzip(h http.Header) bool {
	for _, v := range h.Values("Accept-Encoding") {
		for enc := range strings.SplitSeq(v, ",") {
			name, _, _ := strings.Cut(strings.TrimSpace(enc), ";")
			if strings.EqualFold(name, "gzip") {
				return true
			}
		}
	}
	return false
}

// minCompressBytes is the smallest body worth compressing; below it, gzip's
// framing outweighs the savings.
const minCompressBytes = 1024

// encode serializes v as JSON, gzip-compressed when compress is set and the
// body is large enough to benefit. HTML characters are left unescaped:
// escaping "<" as "\u003c" bloats source code for no benefit in an API body.
func encode(v any, compress bool) (body, error) {
	var raw bytes.Buffer
	enc := json.NewEncoder(&raw)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return body{}, err
	}
	if !compress || raw.Len() < minCompressBytes {
		return body{data: raw.Bytes()}, nil
	}

	var buf bytes.Buffer
	buf.Grow(raw.Len() / 3)
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw.Bytes()); err != nil {
		return body{}, err
	}
	if err := zw.Close(); err != nil {
		return body{}, err
	}
	return body{data: buf.Bytes(), gzip: true}, nil
}

// progressReader reports how much of a request body has been read.
type progressReader struct {
	r     *bytes.Reader
	total int
	fn    func(sent, total int)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.fn(p.total-p.r.Len(), p.total)
	}
	return n, err
}

// envelope is the server's standard response wrapper.
type envelope struct {
	OK    *bool           `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func statusError(status int, data []byte) *StatusError {
	se := &StatusError{StatusCode: status}
	var env envelope
	if json.Unmarshal(data, &env) == nil && env.Error != nil {
		se.Code, se.Message = env.Error.Code, env.Error.Message
		return se
	}
	msg := strings.TrimSpace(string(data))
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	se.Message = msg
	return se
}

// decode unwraps an {ok, data} envelope when present, and otherwise decodes
// the body directly, so the client works with servers that answer either way.
func decode(data []byte, out any) error {
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	var env envelope
	if err := json.Unmarshal(data, &env); err == nil && env.OK != nil {
		if !*env.OK {
			if env.Error != nil {
				return errors.New(env.Error.Message)
			}
			return errors.New("server reported a failure")
		}
		data = env.Data
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func retryable(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	if se, ok := errors.AsType[*StatusError](err); ok {
		return se.Temporary()
	}
	// Anything else is a transport failure: refused, reset, timed out.
	return true
}

// backoff returns 500ms, 1s, 2s, ... with up to 50% jitter so that many
// clients recovering from the same outage do not retry in lockstep.
func backoff(attempt int) time.Duration {
	base := 500 * time.Millisecond << (attempt - 1)
	return base + rand.N(base/2)
}
