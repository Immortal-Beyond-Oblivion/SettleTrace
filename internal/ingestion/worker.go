package ingestion

import (
	"bytes"
	"context"
	"os"
	"path/filepath"

	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/schema"
)

// ProcessLandingDir ingests local files and moves acknowledged objects out of the landing zone.
func (pipeline Pipeline) ProcessLandingDir(ctx context.Context, root string) (Result, error) {
	jobs, err := LoadLandingDir(root)
	if err != nil {
		return Result{}, err
	}
	applied := 0
	for _, job := range jobs {
		envelope := Envelope{Source: job.Source, Body: job.Body, ObjectKey: job.Path, HMACSecret: pipeline.Secret}
		if job.Source == schema.SourceWebhook {
			signature, ok, sigErr := signatureBeside(job.Path)
			if sigErr != nil {
				return Result{}, sigErr
			}
			if !ok {
				continue
			}
			envelope.SignatureHex = signature
		}
		result, processErr := pipeline.Process(ctx, envelope)
		if processErr != nil || !result.ShouldAck() {
			if moveErr := moveBeside(job.Path, root, "failed"); moveErr != nil {
				return Result{}, moveErr
			}
			continue
		}
		if err := moveBeside(job.Path, root, "processed"); err != nil {
			return Result{}, err
		}
		applied += result.Applied
	}
	return Result{Status: StatusApplied, Applied: applied}, nil
}

// ProcessQueue drains one receive batch and deletes a message only after a successful commit.
func (pipeline Pipeline) ProcessQueue(ctx context.Context, queue Queue, objects ObjectStore) error {
	messages, err := queue.Receive(ctx)
	if err != nil {
		return err
	}
	for _, message := range messages {
		decoded := message
		if message.Bucket != "" && message.Key != "" && objects != nil && len(message.Body) == 0 {
			body, getErr := objects.Get(ctx, message.Bucket, message.Key)
			if getErr != nil {
				return getErr
			}
			decoded.Body = body
			if decoded.Source == "" {
				decoded.Source, _ = sourceFromPath("/" + message.Key)
			}
		}
		result, processErr := pipeline.Process(ctx, Envelope{
			Source:       decoded.Source,
			Body:         decoded.Body,
			SignatureHex: decoded.Signature,
			HMACSecret:   pipeline.Secret,
			ObjectKey:    decoded.Key,
		})
		if processErr != nil || !result.ShouldAck() {
			continue
		}
		if err := queue.Delete(ctx, decoded.Receipt); err != nil {
			return err
		}
	}
	return nil
}

// signatureBeside loads a companion .sig file containing the hex HMAC for a local webhook payload.
func signatureBeside(path string) (string, bool, error) {
	body, err := os.ReadFile(path + ".sig")
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return string(bytes.TrimSpace(body)), true, nil
}

// moveBeside relocates a landing file into processed or failed without deleting rejected evidence.
func moveBeside(path, root, bucket string) error {
	targetDir := filepath.Join(root, bucket)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}
	return os.Rename(path, filepath.Join(targetDir, filepath.Base(path)))
}
