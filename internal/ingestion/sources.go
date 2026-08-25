package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/schema"
)

// FileJob is a local landing-zone object ready for pipeline processing.
type FileJob struct {
	Path   string
	Source string
	Body   []byte
}

// LoadLandingDir reads settlement, bank, ledger, and webhook files from a local landing tree.
func LoadLandingDir(root string) ([]FileJob, error) {
	jobs := make([]FileJob, 0)
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			base := strings.ToLower(info.Name())
			if base == "processed" || base == "failed" {
				return filepath.SkipDir
			}
			return nil
		}
		source, ok := sourceFromPath(path)
		if !ok {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		jobs = append(jobs, FileJob{Path: path, Source: source, Body: body})
		return nil
	})
	return jobs, err
}

// sourceFromPath maps landing-zone folders onto ingest source names.
func sourceFromPath(path string) (string, bool) {
	normalized := strings.ToLower(filepath.ToSlash(path))
	switch {
	case strings.Contains(normalized, "/webhooks/") || strings.Contains(normalized, "/webhook/"):
		return schema.SourceWebhook, true
	case strings.Contains(normalized, "/settlements/") || strings.Contains(normalized, "/settlement/"):
		return schema.SourceSettlement, true
	case strings.Contains(normalized, "/ledgers/") || strings.Contains(normalized, "/ledger/"):
		return schema.SourceLedger, true
	case strings.Contains(normalized, "/banks/") || strings.Contains(normalized, "/bank/"):
		return schema.SourceBank, true
	default:
		return "", false
	}
}

// QueueMessage is a transport-neutral ingest job from SQS or an equivalent queue.
type QueueMessage struct {
	ID        string
	Receipt   string
	Body      []byte
	Signature string
	Source    string
	Bucket    string
	Key       string
}

// DecodeQueueBody accepts either a direct ingest envelope or an S3 event notification.
func DecodeQueueBody(body []byte) (QueueMessage, error) {
	var direct struct {
		Source    string `json:"source"`
		Bucket    string `json:"bucket"`
		Key       string `json:"key"`
		Signature string `json:"signature"`
		Payload   json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(body, &direct); err == nil && (direct.Source != "" || direct.Key != "" || len(direct.Payload) > 0) {
		source := direct.Source
		if source == "" {
			source, _ = sourceFromPath("/" + direct.Key)
		}
		return QueueMessage{Body: direct.Payload, Signature: direct.Signature, Source: source, Bucket: direct.Bucket, Key: direct.Key}, nil
	}
	var event struct {
		Records []struct {
			S3 struct {
				Bucket struct {
					Name string `json:"name"`
				} `json:"bucket"`
				Object struct {
					Key string `json:"key"`
				} `json:"object"`
			} `json:"s3"`
		} `json:"Records"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		return QueueMessage{}, fmt.Errorf("unsupported queue body")
	}
	if len(event.Records) == 0 {
		return QueueMessage{}, fmt.Errorf("s3 event contained no records")
	}
	key := event.Records[0].S3.Object.Key
	source, _ := sourceFromPath("/" + key)
	return QueueMessage{
		Source: source,
		Bucket: event.Records[0].S3.Bucket.Name,
		Key:    key,
	}, nil
}

// ObjectStore reads bytes from a landing bucket (LocalStack or AWS S3).
type ObjectStore interface {
	Get(ctx context.Context, bucket, key string) ([]byte, error)
}

// Queue is the acknowledge-after-commit transport for ingest jobs.
type Queue interface {
	Receive(ctx context.Context) ([]QueueMessage, error)
	Delete(ctx context.Context, receipt string) error
}
