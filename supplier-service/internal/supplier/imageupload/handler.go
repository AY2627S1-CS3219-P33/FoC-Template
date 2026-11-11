// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-06
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

// Package imageupload implements administrator image uploads (F2.2.1, F2.3.1).
package imageupload

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/auth"
)

const MaxImageBytes = 5 * 1024 * 1024

type ObjectWriter interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

// A zero Handler keeps the route available with a 503 when uploads are disabled.
type Handler struct {
	client  ObjectWriter
	bucket  string
	baseURL string
}

func NewHandler(client ObjectWriter, bucket, baseURL string) Handler {
	return Handler{client: client, bucket: bucket, baseURL: strings.TrimRight(baseURL, "/")}
}

func (h Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /suppliers/images", h.upload)
}

func (h Handler) upload(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, apperror.Unauthenticated, "authentication is required")
		return
	}
	if !principal.Has(auth.ReadSuppliers) || !principal.Has(auth.ManageSuppliers) {
		writeError(w, http.StatusForbidden, apperror.Forbidden, "insufficient permissions for this operation")
		return
	}
	if h.client == nil || h.bucket == "" || h.baseURL == "" {
		writeError(w, http.StatusServiceUnavailable, apperror.DependencyUnavailable, "image uploads are not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxImageBytes+64*1024)
	if err := r.ParseMultipartForm(MaxImageBytes + 64*1024); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, apperror.InvalidArgument, "image must be at most 5 MiB")
		} else {
			writeError(w, http.StatusBadRequest, apperror.InvalidArgument, "expected multipart/form-data with one image file")
		}
		return
	}
	defer r.MultipartForm.RemoveAll()
	if len(r.MultipartForm.File) != 1 || len(r.MultipartForm.File["image"]) != 1 || len(r.MultipartForm.Value) != 0 {
		writeError(w, http.StatusBadRequest, apperror.InvalidArgument, "provide exactly one image file in the image field")
		return
	}
	file, err := r.MultipartForm.File["image"][0].Open()
	if err != nil {
		writeError(w, http.StatusBadRequest, apperror.InvalidArgument, "image cannot be read")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxImageBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, apperror.InvalidArgument, "image cannot be read")
		return
	}
	if len(data) > MaxImageBytes {
		writeError(w, http.StatusRequestEntityTooLarge, apperror.InvalidArgument, "image must be at most 5 MiB")
		return
	}
	dimensions, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "jpeg" && format != "png") {
		writeError(w, http.StatusBadRequest, apperror.InvalidArgument, "image must be a valid JPEG or PNG")
		return
	}
	// Check dimensions before decoding to bound memory used by compressed images.
	if dimensions.Width <= 0 || dimensions.Height <= 0 || dimensions.Width > 16_000_000/dimensions.Height {
		writeError(w, http.StatusBadRequest, apperror.InvalidArgument, "image must contain at most 16 million pixels")
		return
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		writeError(w, http.StatusBadRequest, apperror.InvalidArgument, "image must be a valid JPEG or PNG")
		return
	}
	key := "suppliers/" + rand.Text() + "." + format
	// shortcut: unattached uploads are retained; add reference-aware cleanup when storage usage warrants it.
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	_, err = h.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(h.bucket), Key: aws.String(key), Body: bytes.NewReader(data),
		ContentType: aws.String("image/" + format), ContentLength: aws.Int64(int64(len(data))),
		CacheControl:         aws.String("public, max-age=31536000, immutable"),
		ServerSideEncryption: types.ServerSideEncryptionAes256,
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, apperror.DependencyUnavailable, "image upload failed; please retry")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{"imageUrl": h.baseURL + "/" + key})
}

func writeError(w http.ResponseWriter, status int, code apperror.Code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apperror.Error{Code: code, Message: message})
}
