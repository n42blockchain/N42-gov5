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
		if r := recover(); r != nil {
			if resp != nil && resp.Body != nil {
				resp.Body.Close()
				resp.Body = nil
			}

			err = fmt.Errorf("http client panic: %s", r)
		}
	}()

	insertCloudflareHeaders(req)

	resp, err = r.Transport.RoundTrip(req)

	attempts := 1
	retry := true

	const minDelay = 500 * time.Millisecond
	const maxDelay = 5 * time.Second
	const maxAttempts = 10

	for err == nil && retry {
		r.downloader.stats.WebseedTripCount.Add(1)

		switch resp.StatusCode {
		case http.StatusOK:
			if len(req.Header.Get("Range")) > 0 {
				// the torrent lib is expecting http.StatusPartialContent so it will discard this
				// if this count is higher than 0, its likely there is a server side config issue
				// as it implies that the server is not handling range requests correctly and is just
				// returning the whole file - which the torrent lib can't handle
				//
				// TODO: We could count the bytes - probably need to take this from the req though
				// as its not clear the amount of the content which will be read.  This needs
				// further investigation - if required.
				r.downloader.stats.WebseedDiscardCount.Add(1)
			}

			r.downloader.stats.WebseedBytesDownload.Add(resp.ContentLength)
			retry = false

		// the first two statuses here have been observed from cloudflare
		// during testing.  The remainder are generally understood to be
		// retriable http responses, calcBackoff will use the Retry-After
		// header if its availible
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
			delayTimer := time.NewTimer(calcBackoff(minDelay, maxDelay, attempts, resp))

			select {
			case <-delayTimer.C:
				// Note this assumes the req.Body is nil
				resp, err = r.Transport.RoundTrip(req)
				r.downloader.stats.WebseedTripCount.Add(1)

			case <-req.Context().Done():
				err = req.Context().Err()
			}
			retry = attempts < maxAttempts

		default:
			r.downloader.stats.WebseedBytesDownload.Add(resp.ContentLength)
			retry = false
		}
	}

	return resp, err
}
