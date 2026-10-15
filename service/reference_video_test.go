package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func referenceMovieFixture(version byte, scale uint32, ticks uint64) []byte {
	header := make([]byte, 108)
	header[0] = version
	if version == 0 {
		binary.BigEndian.PutUint32(header[12:16], scale)
		binary.BigEndian.PutUint32(header[16:20], uint32(ticks))
	} else {
		binary.BigEndian.PutUint32(header[20:24], scale)
		binary.BigEndian.PutUint64(header[24:32], ticks)
	}
	box := func(kind string, payload []byte) []byte {
		data := make([]byte, 8+len(payload))
		binary.BigEndian.PutUint32(data[:4], uint32(len(data)))
		copy(data[4:8], kind)
		copy(data[8:], payload)
		return data
	}
	return append(box("ftyp", []byte("isom\x00\x00\x00\x00")), box("moov", box("mvhd", header))...)
}

func TestReferenceVideoMetadataDuration(t *testing.T) {
	for _, version := range []byte{0, 1} {
		duration, err := referenceMP4Duration(referenceMovieFixture(version, 1000, 4250))
		require.NoError(t, err)
		assert.Equal(t, 4.25, duration)
	}
	for _, data := range [][]byte{
		[]byte("not a video"), referenceMovieFixture(0, 0, 4),
		referenceMovieFixture(0, 1000, 0), referenceMovieFixture(1, 1, 3601),
		referenceMovieFixture(1, 1, ^uint64(0)), referenceMovieFixture(2, 1, 4),
		referenceMovieFixture(0, 1000, 4000)[:30],
	} {
		_, err := referenceMP4Duration(data)
		assert.Error(t, err)
	}
}

type referenceVideoTransport func(*http.Request) (*http.Response, error)

func (f referenceVideoTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestReferenceVideoFetchLimitsAndRedaction(t *testing.T) {
	data := referenceMovieFixture(0, 1000, 4250)
	client := &http.Client{Transport: referenceVideoTransport(func(req *http.Request) (*http.Response, error) {
		assert.Empty(t, req.Header.Get("Authorization"))
		assert.Empty(t, req.Header.Get("Cookie"))
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data)), ContentLength: -1, Header: make(http.Header)}, nil
	})}
	duration, err := probeReferenceVideoSeconds(t.Context(), client, "https://cdn.example/extensionless?signature=secret", 1024)
	require.NoError(t, err)
	assert.Equal(t, 4.25, duration)
	_, err = probeReferenceVideoSeconds(t.Context(), client, "https://cdn.example/video", 32)
	require.ErrorContains(t, err, "size limit")
	client.Transport = referenceVideoTransport(func(req *http.Request) (*http.Response, error) { return nil, io.ErrUnexpectedEOF })
	_, err = probeReferenceVideoSeconds(t.Context(), client, "https://cdn.example/video?signature=secret", 1024)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret")
	for _, url := range []string{"file:///etc/passwd", "http://user:pass@cdn.example/video", "http://cdn.example:8080/video"} {
		_, err := probeReferenceVideoSeconds(t.Context(), client, url, 1024)
		assert.Error(t, err)
	}
}

func TestReferenceVideoAlwaysBlocksPrivateDestinations(t *testing.T) {
	fetchSetting := system_setting.GetFetchSetting()
	original := *fetchSetting
	t.Cleanup(func() { *fetchSetting = original })
	fetchSetting.EnableSSRFProtection = false
	for _, url := range []string{"http://127.0.0.1/video", "http://[::1]/video", "http://169.254.169.254/video", "http://rebound.example/video"} {
		client := newReferenceVideoClient(staticSSRFResolver{"rebound.example": {{IP: net.ParseIP("10.0.0.1")}}}, func(context.Context, string, string) (net.Conn, error) {
			t.Fatal("private destination reached dial")
			return nil, nil
		})
		_, err := probeReferenceVideoSeconds(t.Context(), client, url, 1024)
		assert.Error(t, err)
	}
}

func TestReferenceVideoPinsDNSAndBlocksPrivateRedirect(t *testing.T) {
	var calls int
	client := newReferenceVideoClient(staticSSRFResolver{"cdn.example": {{IP: net.ParseIP("8.8.8.8")}}}, func(ctx context.Context, network, address string) (net.Conn, error) {
		calls++
		assert.Equal(t, "8.8.8.8:80", address)
		local, remote := net.Pipe()
		go func() {
			defer remote.Close()
			_, _ = http.ReadRequest(bufio.NewReader(remote))
			_, _ = io.Copy(remote, strings.NewReader("HTTP/1.1 302 Found\r\nLocation: http://127.0.0.1/video\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"))
		}()
		return local, nil
	})
	t.Cleanup(client.CloseIdleConnections)
	_, err := probeReferenceVideoSeconds(t.Context(), client, "http://cdn.example/video", 1024)
	require.Error(t, err)
	assert.Equal(t, 1, calls)
}
