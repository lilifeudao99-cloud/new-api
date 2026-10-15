package service

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// Reference probing is always public-only, even if general fetch protection is
// disabled. Resolve and pin the destination at dial time; never use an outbound
// proxy or forward the caller's authorization/cookies to a media host.
func newReferenceVideoClient(resolver ssrfResolver, dial func(context.Context, string, string) (net.Conn, error)) *http.Client {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	if dial == nil {
		dial = (&net.Dialer{Timeout: 10 * time.Second}).DialContext
	}
	protected := &protectedFetchDialer{resolver: resolver, dialContext: dial, getProtection: func() (*common.SSRFProtection, bool, error) {
		return &common.SSRFProtection{AllowedPorts: []int{80, 443}, ApplyIPFilterForDomain: true}, true, nil
	}}
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{DialContext: protected.DialContext, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 10 * time.Second, MaxIdleConns: 8, IdleConnTimeout: 30 * time.Second},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			req.Header.Del("Referer")
			if len(via) > 3 {
				return errors.New("too many reference video redirects")
			}
			return validateReferenceVideoURL(req.URL)
		},
	}
}

var referenceVideoClient = newReferenceVideoClient(nil, nil)

func validateReferenceVideoURL(u *url.URL) error {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return errors.New("reference video requires a public HTTP(S) URL without credentials")
	}
	if u.Port() != "" && u.Port() != "80" && u.Port() != "443" {
		return errors.New("reference video port must be 80 or 443")
	}
	return nil
}

// ProbeReferenceVideoSeconds reads bounded MP4/QuickTime metadata, not client
// duration hints. It makes no generation request and returns no media URL in errors.
func ProbeReferenceVideoSeconds(ctx context.Context, rawURL string) (float64, error) {
	limit := int64(64 << 20)
	if constant.MaxFileDownloadMB > 0 && constant.MaxFileDownloadMB < 64 {
		limit = int64(constant.MaxFileDownloadMB) << 20
	}
	return probeReferenceVideoSeconds(ctx, referenceVideoClient, rawURL, limit)
}

func probeReferenceVideoSeconds(ctx context.Context, client *http.Client, rawURL string, limit int64) (float64, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return 0, errors.New("invalid reference video URL")
	}
	if err = validateReferenceVideoURL(u); err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, errors.New("invalid reference video request")
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, errors.New("reference video could not be fetched safely within the time limit")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("reference video download returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > limit {
		return 0, errors.New("reference video exceeds the download size limit")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return 0, errors.New("reference video download was incomplete")
	}
	if int64(len(data)) > limit {
		return 0, errors.New("reference video exceeds the download size limit")
	}
	return referenceMP4Duration(data)
}

// Read only top-level/moov boxes, avoiding allocation from untrusted MP4 sample
// counts or recursive parsing. Fragmented media without a known movie duration
// and other containers are rejected rather than assigned a guessed duration.
func referenceMP4Duration(data []byte) (float64, error) {
	var duration float64
	var found bool
	for len(data) > 0 {
		kind, payload, rest, err := referenceMP4Box(data)
		if err != nil {
			return 0, err
		}
		data = rest
		if kind != "moov" {
			continue
		}
		for len(payload) > 0 {
			child, value, tail, err := referenceMP4Box(payload)
			if err != nil {
				return 0, err
			}
			payload = tail
			if child != "mvhd" {
				continue
			}
			if found || len(value) < 20 {
				return 0, errors.New("invalid reference video movie duration")
			}
			found = true
			var scale uint32
			var ticks uint64
			switch value[0] {
			case 0:
				scale = binary.BigEndian.Uint32(value[12:16])
				ticks = uint64(binary.BigEndian.Uint32(value[16:20]))
			case 1:
				if len(value) < 32 {
					return 0, errors.New("invalid reference video movie duration")
				}
				scale = binary.BigEndian.Uint32(value[20:24])
				ticks = binary.BigEndian.Uint64(value[24:32])
			default:
				return 0, errors.New("unsupported reference video movie header")
			}
			duration = float64(ticks) / float64(scale)
		}
	}
	if !found || math.IsNaN(duration) || math.IsInf(duration, 0) || duration <= 0 || duration > relaycommon.MaxTaskDurationSeconds {
		return 0, errors.New("cannot verify reference video duration; use MP4/MOV/M4V with a duration between 0 and 3600 seconds")
	}
	return duration, nil
}

func referenceMP4Box(data []byte) (string, []byte, []byte, error) {
	if len(data) < 8 {
		return "", nil, nil, errors.New("invalid or unsupported reference video; use MP4/MOV/M4V")
	}
	size, header := uint64(binary.BigEndian.Uint32(data[:4])), uint64(8)
	if size == 1 {
		if len(data) < 16 {
			return "", nil, nil, errors.New("truncated reference video box")
		}
		size, header = binary.BigEndian.Uint64(data[8:16]), 16
	} else if size == 0 {
		size = uint64(len(data))
	}
	if size < header || size > uint64(len(data)) {
		return "", nil, nil, errors.New("invalid reference video box size")
	}
	return string(data[4:8]), data[header:size], data[size:], nil
}
