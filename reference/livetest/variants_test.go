// Package livetest proves, against the running stack, what the generated
// end-to-end suite cannot: it sends and reads BYTES. A task cover is an
// UPLOADED_IMAGE with declared variants, stored in an S3 bucket (MinIO in
// this stack), and what is worth proving is in the bucket and in the pixels:
// the variants are rendered in the background without anyone asking, are
// served at their declared sizes, are not rendered again on a second
// request, and a missing one is rendered on the spot.
//
// run.sh brings the stack up with `w17ctl test --keep` and runs this package
// inside the stack's network.
package livetest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wandering-compiler/sdk/go/lib/kvfs"
	kvfsstore "github.com/wandering-compiler/sdk/go/lib/kvfs/store"

	"github.com/wandering-compiler/examples/reference/livetest/device"
)

var (
	gateway = env("GATEWAY_URL", "http://app-gateway:8080")
	// bucket is the gateway's own upload root (compose.reference.yaml), read
	// back to see what the gateway stored.
	bucket = env("UPLOADS_ROOT", "s3://minioadmin:minioadmin@app-uploads?endpoint=http://minio:9000&region=us-east-1&path_style=true")
	// gatewayOrigin is the gateway's configured public origin, which device
	// proofs name (run.sh reads it from the stack); "" when the stack
	// declares none, and then the proofs name GATEWAY_URL's origin.
	gatewayOrigin = env("GATEWAY_DPOP_ORIGIN", "")

	// api sends the REST calls: with device proofs when the gateway binds
	// tokens to device keys, as Bearer tokens when it does not.
	api   *device.Client
	store kvfs.Driver
)

// The download prefix covers rewrite the stored key under (rest.proto: the
// `/files` download, RELATIVE form).
const filesPrefix = "/api/v1/tasks/files/"

// A task the fixtures seed (fixtures/app/test.json).
const taskID = 700001

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func TestMain(m *testing.M) {
	var err error
	if api, err = device.New(http.DefaultClient, gateway, gatewayOrigin); err != nil {
		fmt.Fprintln(os.Stderr, "livetest:", err)
		os.Exit(1)
	}
	if store, err = kvfsstore.Open(context.Background(), bucket, false); err != nil {
		fmt.Fprintln(os.Stderr, "livetest: the uploads bucket:", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// A pregenerated cover: both variants land in the bucket with no download
// asking for them, are served at their declared sizes and formats, and a
// second request reads the stored one instead of rendering it again.
func TestCoverVariants_RenderedInTheBackgroundAndServedFromTheBucket(t *testing.T) {
	token := signUp(t)
	key := uploadCover(t, token, pngOf(400, 200), "cover.png")

	// Nothing has asked for a variant yet: they appear because the upload
	// queued them.
	thumbBefore := waitForObject(t, key+"~thumb")
	waitForObject(t, key+"~card")

	for _, tc := range []struct {
		variant, contentType string
		w, h                 int
	}{
		// The original, untouched — and, since this is an image field, served
		// as the raster type its bytes are (sdk #75): the gateway sniffs
		// JPEG / PNG / GIF / WebP from the bytes, never from the filename,
		// so an uploaded HTML or SVG still goes out as
		// application/octet-stream, nosniff always. A variant is decoded and
		// re-encoded by the server, so it is the JPEG or PNG its
		// Content-Type says.
		{"", "image/png", 400, 200},
		{"thumb", "image/png", 64, 32},
		{"card", "image/jpeg", 320, 160},
	} {
		body, ct := download(t, key, tc.variant, http.StatusOK)
		if ct != tc.contentType {
			t.Errorf("variant %q: Content-Type %q, want %q", tc.variant, ct, tc.contentType)
		}
		if w, h := dims(t, body, tc.contentType); w != tc.w || h != tc.h {
			t.Errorf("variant %q: %dx%d, want %dx%d", tc.variant, w, h, tc.w, tc.h)
		}
	}

	// The second request for thumb read what the background had stored:
	// the object is the one written before any download, untouched.
	download(t, key, "thumb", http.StatusOK)
	thumbAfter := stat(t, key+"~thumb")
	if !thumbAfter.ModTime.Equal(thumbBefore.ModTime) || thumbAfter.Size != thumbBefore.Size {
		t.Errorf("thumb was written again by a download: %+v, then %+v", thumbBefore, thumbAfter)
	}
}

// A variant that is not in the bucket — the background job lost with a
// restart, or a queue that was full — is rendered on its first request and
// stored, so nothing depends on the background having finished.
func TestCoverVariants_AMissingOneIsRenderedOnTheSpot(t *testing.T) {
	token := signUp(t)
	key := uploadCover(t, token, pngOf(200, 200), "square.png")
	waitForObject(t, key+"~thumb")
	if err := store.Delete(context.Background(), key+"~thumb"); err != nil {
		t.Fatalf("delete the stored thumb: %v", err)
	}
	body, ct := download(t, key, "thumb", http.StatusOK)
	if w, h := dims(t, body, ct); w != 64 || h != 64 {
		t.Errorf("thumb rendered on the spot: %dx%d, want 64x64", w, h)
	}
	stat(t, key+"~thumb") // stored again, for the next viewer
}

// Only declared names exist: anything else is a 400, so no URL makes the
// server render and store an image of a size nobody declared. A file that
// is not an image has no variants: 415.
func TestCoverVariants_RefusesAnUndeclaredVariantAndANonImage(t *testing.T) {
	token := signUp(t)
	key := uploadCover(t, token, pngOf(100, 100), "small.png")
	download(t, key, "huge", http.StatusBadRequest)

	text := uploadCover(t, token, []byte("this is not an image"), "fake.png")
	download(t, text, "thumb", http.StatusUnsupportedMediaType)
	if _, err := store.Stat(context.Background(), text+"~thumb"); !errors.Is(err, kvfs.ErrNotFound) {
		t.Errorf("a variant of a non-image was stored (stat: %v)", err)
	}
}

func signUp(t *testing.T) string {
	t.Helper()
	var resp struct {
		Token string `json:"token"`
	}
	body, _ := json.Marshal(map[string]string{
		"email":    fmt.Sprintf("live-%d@reference.example", time.Now().UnixNano()),
		"password": "e2eSecret!23",
	})
	// The sign-up proves a new device key: one per account.
	var session *device.Session
	raw := exchange(t, http.MethodPost, "/api/v1/auth/tenant-alpha/register", "application/json", bytes.NewReader(body), http.StatusOK,
		func(req *http.Request) (resp *http.Response, err error) {
			resp, session, err = api.Mint(req)
			return resp, err
		})
	if err := json.Unmarshal(raw, &resp); err != nil || resp.Token == "" {
		t.Fatalf("sign-up answer %s: %v", raw, err)
	}
	api.Keep(resp.Token, session)
	return resp.Token
}

// uploadCover uploads an image as task 700001's cover and returns the stored
// object's key, read back through the cover's download URL.
func uploadCover(t *testing.T, token string, content []byte, filename string) string {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("image", filename)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(content)
	_ = mw.Close()
	var created struct {
		ID string `json:"id"`
	}
	raw := send(t, http.MethodPost, fmt.Sprintf("/api/v1/tasks/%d/cover", taskID), token, mw.FormDataContentType(), &buf, http.StatusOK)
	if err := json.Unmarshal(raw, &created); err != nil || created.ID == "" {
		t.Fatalf("upload answer %s: %v", raw, err)
	}
	var cover struct {
		ImagePath string `json:"image_path"`
	}
	raw = send(t, http.MethodGet, "/api/v1/tasks/covers/"+created.ID, token, "", nil, http.StatusOK)
	if err := json.Unmarshal(raw, &cover); err != nil {
		t.Fatalf("cover answer %s: %v", raw, err)
	}
	key, ok := strings.CutPrefix(cover.ImagePath, filesPrefix)
	if !ok || key == "" {
		t.Fatalf("cover image_path %q is not a URL under %s", cover.ImagePath, filesPrefix)
	}
	return key
}

// download fetches the stored object (variant "" is the original) without
// a token: the `/files` download is public.
func download(t *testing.T, key, variant string, want int) ([]byte, string) {
	t.Helper()
	url := gateway + filesPrefix + key
	if variant != "" {
		url += "?variant=" + variant
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := api.Do(req, "")
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("GET %s: status %d, want %d: %.200s", url, resp.StatusCode, want, body)
	}
	return body, resp.Header.Get("Content-Type")
}

// send makes one REST call on behalf of token ("" for none), with a proof
// by its device key when the gateway bound it to one.
func send(t *testing.T, method, path, token, contentType string, body io.Reader, want int) []byte {
	t.Helper()
	return exchange(t, method, path, contentType, body, want, func(req *http.Request) (*http.Response, error) {
		return api.Do(req, token)
	})
}

// exchange builds one REST request, sends it with do and returns the answer,
// failing the test unless the status is want.
func exchange(t *testing.T, method, path, contentType string, body io.Reader, want int, do func(*http.Request) (*http.Response, error)) []byte {
	t.Helper()
	req, err := http.NewRequest(method, gateway+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("%s %s: status %d, want %d: %s", method, path, resp.StatusCode, want, raw)
	}
	return raw
}

// waitForObject waits for the gateway's background workers to store key.
func waitForObject(t *testing.T, key string) kvfs.Info {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		info, err := store.Stat(context.Background(), key)
		if err == nil {
			return info
		}
		if !errors.Is(err, kvfs.ErrNotFound) {
			t.Fatalf("stat %s: %v", key, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never appeared in the bucket: the variant was not rendered in the background", key)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func stat(t *testing.T, key string) kvfs.Info {
	t.Helper()
	info, err := store.Stat(context.Background(), key)
	if err != nil {
		t.Fatalf("stat %s: %v", key, err)
	}
	return info
}

func pngOf(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 128, 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func dims(t *testing.T, body []byte, contentType string) (int, int) {
	t.Helper()
	var cfg image.Config
	var err error
	switch contentType {
	case "application/octet-stream":
		cfg, _, err = image.DecodeConfig(bytes.NewReader(body))
	case "image/jpeg":
		cfg, err = jpeg.DecodeConfig(bytes.NewReader(body))
	default:
		cfg, err = png.DecodeConfig(bytes.NewReader(body))
	}
	if err != nil {
		t.Fatalf("decode %s: %v", contentType, err)
	}
	return cfg.Width, cfg.Height
}
