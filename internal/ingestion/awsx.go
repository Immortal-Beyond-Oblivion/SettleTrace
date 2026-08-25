package ingestion

import (
	"context"
	"io"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

// S3Store reads landing objects from AWS S3 or LocalStack.
type S3Store struct {
	client *s3.Client
}

// SQSQueue receives and deletes ingest jobs from AWS SQS or LocalStack.
type SQSQueue struct {
	client   *sqs.Client
	queueURL string
}

// NewLocalAWSClients builds S3 and SQS clients using the shared endpoint override when present.
func NewLocalAWSClients(ctx context.Context) (aws.Config, error) {
	opts := []func(*config.LoadOptions) error{
		config.WithRegion(firstNonEmpty(os.Getenv("AWS_REGION"), "ap-south-1")),
	}
	endpoint := os.Getenv("AWS_ENDPOINT_URL")
	if endpoint != "" {
		opts = append(opts, config.WithBaseEndpoint(endpoint))
	}
	return config.LoadDefaultConfig(ctx, opts...)
}

// NewS3Store constructs an object reader from a loaded AWS config.
func NewS3Store(cfg aws.Config) *S3Store {
	return &S3Store{client: s3.NewFromConfig(cfg, func(options *s3.Options) {
		if os.Getenv("AWS_ENDPOINT_URL") != "" {
			options.UsePathStyle = true
		}
	})}
}

// Get downloads one object from the landing bucket.
func (store *S3Store) Get(ctx context.Context, bucket, key string) ([]byte, error) {
	output, err := store.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return nil, err
	}
	defer output.Body.Close()
	return io.ReadAll(output.Body)
}

// NewSQSQueue constructs a queue consumer for a configured queue URL.
func NewSQSQueue(cfg aws.Config, queueURL string) *SQSQueue {
	return &SQSQueue{client: sqs.NewFromConfig(cfg), queueURL: queueURL}
}

// Receive pulls a small batch of ingest messages.
func (queue *SQSQueue) Receive(ctx context.Context) ([]QueueMessage, error) {
	output, err := queue.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(queue.queueURL),
		MaxNumberOfMessages: 5,
		WaitTimeSeconds:     5,
	})
	if err != nil {
		return nil, err
	}
	messages := make([]QueueMessage, 0, len(output.Messages))
	for _, message := range output.Messages {
		body := []byte(aws.ToString(message.Body))
		decoded, decodeErr := DecodeQueueBody(body)
		if decodeErr != nil {
			decoded = QueueMessage{Body: body}
		}
		decoded.ID = aws.ToString(message.MessageId)
		decoded.Receipt = aws.ToString(message.ReceiptHandle)
		if message.MessageAttributes != nil {
			if signature, ok := message.MessageAttributes["signature"]; ok && signature.StringValue != nil {
				decoded.Signature = *signature.StringValue
			}
		}
		messages = append(messages, decoded)
	}
	return messages, nil
}

// Delete acknowledges a message after the ingest transaction committed.
func (queue *SQSQueue) Delete(ctx context.Context, receipt string) error {
	_, err := queue.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(queue.queueURL),
		ReceiptHandle: aws.String(receipt),
	})
	return err
}
