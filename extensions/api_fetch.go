//go:build !noext

package extensions

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/dop251/goja"
)

const (
	defaultFetchTimeout = 30 * time.Second
	maxFetchBody        = 10 << 20
)

// jsFetch is a minimal fetch(url, {method, headers, body, timeout}):
// it resolves to {status, ok, headers, text(), json()} with the body read
// (up to 10 MiB). headers has lower-case names. It rejects on network
// errors, not on HTTP error statuses, like fetch.
func (e *ext) jsFetch(url string, opts *goja.Object) goja.Value {
	e.readOnlyRender()
	method := strings.ToUpper(optString(opts, "method"))
	if method == "" {
		method = "GET"
	}
	body := optString(opts, "body")
	headers := map[string]string{}
	if opts != nil {
		if h := opts.Get("headers"); h != nil && !goja.IsUndefined(h) && !goja.IsNull(h) {
			if err := e.vm.ExportTo(h, &headers); err != nil {
				panic(e.vm.NewTypeError("fetch: headers must be an object of strings"))
			}
		}
	}
	timeout := defaultFetchTimeout
	if ms := optNumber(opts, "timeout"); ms > 0 {
		timeout = time.Duration(ms * float64(time.Millisecond))
	}
	return e.async(func() (func() goja.Value, error) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, rd)
		if err != nil {
			return nil, err
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBody))
		if err != nil {
			return nil, err
		}
		text := string(data)
		return func() goja.Value {
			vm := e.vm
			o := vm.NewObject()
			_ = o.Set("status", resp.StatusCode)
			_ = o.Set("ok", resp.StatusCode/100 == 2)
			h := vm.NewObject()
			for k := range resp.Header {
				_ = h.Set(strings.ToLower(k), resp.Header.Get(k))
			}
			_ = o.Set("headers", h)
			_ = o.Set("text", func() goja.Value { return e.resolved(vm.ToValue(text)) })
			_ = o.Set("json", func() goja.Value {
				var v any
				if err := json.Unmarshal(data, &v); err != nil {
					return e.rejected(err)
				}
				return e.resolved(vm.ToValue(v))
			})
			return o
		}, nil
	})
}

// resolved and rejected are settled promises, for APIs that are async
// in the browser.
func (e *ext) resolved(v goja.Value) goja.Value {
	p, resolve, _ := e.vm.NewPromise()
	_ = resolve(v)
	return e.vm.ToValue(p)
}

func (e *ext) rejected(err error) goja.Value {
	p, _, reject := e.vm.NewPromise()
	_ = reject(e.vm.NewGoError(err))
	return e.vm.ToValue(p)
}
