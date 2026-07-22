package app

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const daemonMaxImageAttachmentBytes = 5 * 1024 * 1024

var daemonAllowedImageTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
	"image/gif":  ".gif",
}

func prepareImageAttachments(ctx context.Context, attachments []messageAttachment) ([]messageAttachment, string, error) {
	if len(attachments) == 0 {
		return nil, "", nil
	}
	tempDir, err := os.MkdirTemp("", "pocketcode-images-*")
	if err != nil {
		return nil, "", err
	}
	prepared := make([]messageAttachment, 0, len(attachments))
	for _, attachment := range attachments {
		if strings.TrimSpace(attachment.Type) != "image" {
			_ = os.RemoveAll(tempDir)
			return nil, "", fmt.Errorf("unsupported attachment type %q", attachment.Type)
		}
		localPath, localData, err := downloadImageAttachment(ctx, tempDir, attachment)
		if err != nil {
			_ = os.RemoveAll(tempDir)
			return nil, "", err
		}
		attachment.LocalPath = localPath
		attachment.LocalData = localData
		prepared = append(prepared, attachment)
	}
	return prepared, tempDir, nil
}

func downloadImageAttachment(ctx context.Context, tempDir string, attachment messageAttachment) (string, string, error) {
	attachmentURL := strings.TrimSpace(attachment.URL)
	parsed, err := url.Parse(attachmentURL)
	if err != nil || parsed == nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || strings.TrimSpace(parsed.Host) == "" {
		return "", "", fmt.Errorf("invalid image attachment url")
	}
	mimeType, ext, err := normalizeDaemonImageMimeType(attachment.MimeType, attachment.Filename)
	if err != nil {
		return "", "", err
	}
	if attachment.SizeBytes <= 0 || attachment.SizeBytes > daemonMaxImageAttachmentBytes {
		return "", "", fmt.Errorf("image attachment must be 5MB or smaller")
	}

	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, attachmentURL, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", "", fmt.Errorf("image attachment download failed: %d", resp.StatusCode)
	}
	if contentType := strings.TrimSpace(resp.Header.Get("Content-Type")); contentType != "" {
		if parsedType, _, parseErr := mime.ParseMediaType(contentType); parseErr == nil {
			parsedType = strings.ToLower(strings.TrimSpace(parsedType))
			if parsedType != mimeType {
				return "", "", fmt.Errorf("image attachment content-type mismatch: %s", parsedType)
			}
		}
	}

	payload, err := io.ReadAll(io.LimitReader(resp.Body, daemonMaxImageAttachmentBytes+1))
	if err != nil {
		return "", "", err
	}
	if len(payload) == 0 {
		return "", "", fmt.Errorf("image attachment is empty")
	}
	if len(payload) > daemonMaxImageAttachmentBytes {
		return "", "", fmt.Errorf("image attachment exceeds 5MB")
	}
	detected := http.DetectContentType(payload)
	if detectedType, _, parseErr := mime.ParseMediaType(detected); parseErr == nil {
		detectedType = strings.ToLower(strings.TrimSpace(detectedType))
		if _, ok := daemonAllowedImageTypes[detectedType]; ok && detectedType != mimeType {
			return "", "", fmt.Errorf("image attachment bytes are %s, expected %s", detectedType, mimeType)
		}
	}

	name := strings.TrimSpace(attachment.ID)
	if name == "" {
		name = "image"
	}
	name = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			return r
		}
		return '-'
	}, name)
	localPath := filepath.Join(tempDir, name+ext)
	if err := os.WriteFile(localPath, payload, 0o600); err != nil {
		return "", "", err
	}
	return localPath, base64.StdEncoding.EncodeToString(payload), nil
}

func normalizeDaemonImageMimeType(rawMimeType string, filename string) (string, string, error) {
	mimeType := strings.ToLower(strings.TrimSpace(rawMimeType))
	if parsed, _, err := mime.ParseMediaType(mimeType); err == nil {
		mimeType = strings.ToLower(strings.TrimSpace(parsed))
	}
	if ext, ok := daemonAllowedImageTypes[mimeType]; ok {
		return mimeType, ext, nil
	}
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(filename)))
	for allowed, allowedExt := range daemonAllowedImageTypes {
		if ext == allowedExt || (ext == ".jpeg" && allowed == "image/jpeg") {
			return allowed, allowedExt, nil
		}
	}
	return "", "", fmt.Errorf("unsupported image attachment type %q", rawMimeType)
}
