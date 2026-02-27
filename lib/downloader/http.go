package downloader

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"
)

var cloudflareHeaders = http.Header{
	"lsjdjwcush6jbnjj3jnjscoscisoc5s": []string{"I%OSJDNFKE783DDHHJD873EFSIVNI7384R78SSJBJBCCJBC32JABBJCBJK45"},
}

func insertCloudflareHeaders(req *http.Request) {
	for key, value := range cloudflareHeaders {
		req.Header[key] = value
	}
}

// retryBackoff performs exponential backoff based on the attempt number and limited
// by the provided minimum and maximum durations.
//
// It also tries to parse Retry-After response header when a http.StatusTooManyRequests
// (HTTP Code 429) is found in the resp parameter. Hence it will return the number of
// seconds the server states it may be ready to process more requests from this client.
func calcBackoff(min, max time.Duration, attemptNum int, resp *http.Response) time.Duration {
	if resp != nil {
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
			if s, ok := resp.Header["Retry-After"]; ok {
				if sleep, err := strconv.ParseInt(s[0], 10, 64); err == nil {
					if sleep < 0 {
						sleep = 0
					}
					duration := time.Second * time.Duration(sleep)
					if duration > max {
						duration = max
					}
					return duration
				}
			}
		}
	}

	mult := math.Pow(2, float64(attemptNum)) * float64(min)
	sleep := time.Duration(mult)
	if float64(sleep) != mult || sleep > max {
		sleep = max
	}

	return sleep
}

type requestHandler struct {
	http.Transport
	downloader *Downloader
}

func (r *requestHandler) RoundTrip(req *http.Request) (resp *http.Response, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			if resp != nil && resp.Body != nil {
				resp.Body.Close()
				resp.Body = nil
			}
			err = fmt.Errorf("http client panic: %s", rec)
		}
	}()

	insertCloudflareHeaders(req)
	resp, err = r.Transport.RoundTrip(req)

	const (
		minDelay    = 500 * time.Millisecond
		maxDelay    = 5 * time.Second
		maxAttempts = 10
	)

	for attempts := 1; err == nil; {
		r.downloader.stats.WebseedTripCount.Add(1)

		switch resp.StatusCode {
		case http.StatusOK:
			// Server returned 200 for a range request -- it's ignoring the Range header.
			// The torrent lib expects 206 and will discard this response.
			if req.Header.Get("Range") != "" {
				r.downloader.stats.WebseedDiscardCount.Add(1)
			}
			r.downloader.stats.WebseedBytesDownload.Add(resp.ContentLength)
			return resp, nil

		case http.StatusInternalServerError, http.StatusBadGateway,
			http.StatusRequestTimeout, http.StatusTooEarly,
			http.StatusTooManyRequests, http.StatusServiceUnavailable,
			http.StatusGatewayTimeout:

			r.downloader.stats.WebseedServerFails.Add(1)
			if resp.Body != nil {
				resp.Body.Close()
				resp.Body = nil
			}

			attempts++
			if attempts > maxAttempts {
				return resp, err
			}

			delayTimer := time.NewTimer(calcBackoff(minDelay, maxDelay, attempts, resp))
			select {
			case <-delayTimer.C:
				resp, err = r.Transport.RoundTrip(req)
				r.downloader.stats.WebseedTripCount.Add(1)
			case <-req.Context().Done():
				return resp, req.Context().Err()
			}

		default:
			r.downloader.stats.WebseedBytesDownload.Add(resp.ContentLength)
			return resp, nil
		}
	}

	return resp, err
}
