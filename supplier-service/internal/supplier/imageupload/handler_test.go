// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-06
// Scope: Implementation of test code (unit and integration tests).
// Author review: Reviewed, validated, and tested by the authors.

package imageupload

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/auth"
)

func fixture(t *testing.T, format string) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var data bytes.Buffer
	if format == "png" {
		require.NoError(t, png.Encode(&data, img))
	} else {
		require.NoError(t, jpeg.Encode(&data, img, nil))
	}
	return data.Bytes()
}

func request(t *testing.T, data []byte, count int) *http.Request {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for range count {
		part, err := w.CreateFormFile("image", "../../untrusted.svg")
		require.NoError(t, err)
		_, err = part.Write(data)
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	r := httptest.NewRequest(http.MethodPost, "/suppliers/images", &body)
	r.Header.Set("Content-Type", w.FormDataContentType())
	return r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{
		Subject: "admin", Permissions: []auth.Permission{auth.ReadSuppliers, auth.ManageSuppliers},
	}))
}

func handler(t *testing.T, serve http.HandlerFunc) Handler {
	t.Helper()
	server := httptest.NewServer(serve)
	t.Cleanup(server.Close)
	client := s3.NewFromConfig(aws.Config{
		Region: "ap-southeast-1", Credentials: credentials.NewStaticCredentialsProvider("test-key", "test-secret", ""),
		RetryMaxAttempts: 1, RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
	}, func(o *s3.Options) { o.BaseEndpoint = aws.String(server.URL); o.UsePathStyle = true })
	return NewHandler(client, "foc-supplier-images", "https://images.example.com/")
}

func TestUploadUsesActualFormatAndUniqueKeys_F2_2_1(t *testing.T) {
	for _, format := range []string{"jpeg", "png"} {
		t.Run(format, func(t *testing.T) {
			data := fixture(t, format)
			var paths []string
			h := handler(t, func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, http.MethodPut, r.Method)
				require.True(t, strings.HasPrefix(r.URL.Path, "/foc-supplier-images/suppliers/"))
				require.True(t, strings.HasSuffix(r.URL.Path, "."+format))
				require.NotContains(t, r.URL.Path, "untrusted")
				require.Equal(t, "image/"+format, r.Header.Get("Content-Type"))
				require.Equal(t, "AES256", r.Header.Get("X-Amz-Server-Side-Encryption"))
				require.Contains(t, r.Header.Get("Cache-Control"), "immutable")
				require.Empty(t, r.Header.Get("X-Amz-Acl"))
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				require.Equal(t, data, body)
				paths = append(paths, r.URL.Path)
				w.Header().Set("ETag", `"test"`)
				w.WriteHeader(http.StatusOK)
			})
			mux := http.NewServeMux()
			h.RegisterRoutes(mux)
			for range 2 {
				response := httptest.NewRecorder()
				mux.ServeHTTP(response, request(t, data, 1))
				require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
				var result map[string]string
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
				require.Equal(t, "https://images.example.com"+strings.TrimPrefix(paths[len(paths)-1], "/foc-supplier-images"), result["imageUrl"])
			}
			require.Len(t, paths, 2)
			require.NotEqual(t, paths[0], paths[1])
		})
	}
}

func TestRejectsInvalidImagesBeforeS3_NFR3_3(t *testing.T) {
	pngData := fixture(t, "png")
	bomb := bytes.Clone(pngData)
	binary.BigEndian.PutUint32(bomb[16:20], 8000)
	binary.BigEndian.PutUint32(bomb[20:24], 8000)
	binary.BigEndian.PutUint32(bomb[29:33], crc32.ChecksumIEEE(bomb[12:29]))
	h := handler(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("invalid image reached S3")
		w.WriteHeader(500)
	})
	for _, tt := range []struct {
		name          string
		data          []byte
		count, status int
	}{
		{"not an image", []byte("<svg></svg>"), 1, 400},
		{"truncated image", pngData[:33], 1, 400},
		{"dimension bomb", bomb, 1, 400},
		{"oversized file", make([]byte, MaxImageBytes+1), 1, 413},
		{"oversized request", make([]byte, MaxImageBytes+128*1024), 1, 413},
		{"missing file", nil, 0, 400},
		{"multiple files", pngData, 2, 400},
	} {
		t.Run(tt.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			h.upload(response, request(t, tt.data, tt.count))
			require.Equal(t, tt.status, response.Code, response.Body.String())
		})
	}
}

func TestAuthorizationAndDisabledUploads(t *testing.T) {
	for _, tt := range []struct {
		name      string
		principal *auth.Principal
		status    int
	}{
		{"unauthenticated", nil, 401},
		{"read only", &auth.Principal{Subject: "user", Permissions: []auth.Permission{auth.ReadSuppliers}}, 403},
		{"manage only", &auth.Principal{Subject: "admin", Permissions: []auth.Permission{auth.ManageSuppliers}}, 403},
		{"disabled", &auth.Principal{Subject: "admin", Permissions: []auth.Permission{auth.ReadSuppliers, auth.ManageSuppliers}}, 503},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/suppliers/images", nil)
			if tt.principal != nil {
				r = r.WithContext(auth.WithPrincipal(r.Context(), *tt.principal))
			}
			response := httptest.NewRecorder()
			Handler{}.upload(response, r)
			require.Equal(t, tt.status, response.Code)
		})
	}
}

func TestS3FailureIsSafeToRetryAndDoesNotExposeProviderErrors(t *testing.T) {
	h := handler(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "<Error><Code>AccessDenied</Code><Message>provider-secret</Message></Error>")
	})
	response := httptest.NewRecorder()
	h.upload(response, request(t, fixture(t, "png"), 1))
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.NotContains(t, response.Body.String(), "provider-secret")
	require.Contains(t, response.Body.String(), "DEPENDENCY_UNAVAILABLE")
}
