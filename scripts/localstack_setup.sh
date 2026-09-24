#!/usr/bin/env bash
# Wires LocalStack's S3 bucket + SQS queue + bucket-to-queue event notification for local
# development (implementation.md section 23). Run once after `docker compose up -d localstack`.
# Safe to re-run: every step tolerates already-existing resources.
#
#   bash scripts/localstack_setup.sh        (or `chmod +x` it and run it directly)
#
# Overridable through the environment: AWS_ENDPOINT_URL, RECON_BUCKET, RECON_QUEUE.
set -euo pipefail

ENDPOINT="${AWS_ENDPOINT_URL:-http://localhost:4566}"
BUCKET="${RECON_BUCKET:-recon-landing}"
QUEUE="${RECON_QUEUE:-recon-ingestion-queue}"

# LocalStack accepts any credentials, but the AWS CLI refuses to run without some and without a
# region. Default to dummy values (and docker-compose.yml's region) only when none are set, so a
# developer's real AWS profile is never overridden.
export AWS_ACCESS_KEY_ID="${AWS_ACCESS_KEY_ID:-test}"
export AWS_SECRET_ACCESS_KEY="${AWS_SECRET_ACCESS_KEY:-test}"
export AWS_DEFAULT_REGION="${AWS_DEFAULT_REGION:-ap-south-1}"

awslocal() {
  aws --endpoint-url="$ENDPOINT" "$@"
}

echo "Waiting for LocalStack at $ENDPOINT ..."
ready=""
for _ in $(seq 1 30); do
  if awslocal s3api list-buckets >/dev/null 2>&1; then
    ready="yes"
    break
  fi
  sleep 2
done
if [ -z "$ready" ]; then
  echo "LocalStack did not become ready at $ENDPOINT. Is 'docker compose up -d localstack' running?" >&2
  exit 1
fi

# s3 mb fails when the bucket already exists, so create it only if head-bucket says it is absent.
if awslocal s3api head-bucket --bucket "$BUCKET" >/dev/null 2>&1; then
  echo "Bucket s3://$BUCKET already exists."
else
  awslocal s3 mb "s3://$BUCKET"
fi

# create-queue is idempotent and returns the queue URL, so there is no need to hardcode the
# LocalStack account id into the URL.
QUEUE_URL=$(awslocal sqs create-queue --queue-name "$QUEUE" --query 'QueueUrl' --output text)

QUEUE_ARN=$(awslocal sqs get-queue-attributes \
  --queue-url "$QUEUE_URL" \
  --attribute-names QueueArn \
  --query 'Attributes.QueueArn' --output text)

awslocal s3api put-bucket-notification-configuration \
  --bucket "$BUCKET" \
  --notification-configuration "{\"QueueConfigurations\":[{\"QueueArn\":\"$QUEUE_ARN\",\"Events\":[\"s3:ObjectCreated:*\"]}]}"

echo "LocalStack S3+SQS wired: s3://$BUCKET -> $QUEUE_ARN"
echo "Test with:"
echo "  aws --endpoint-url=$ENDPOINT s3 cp ./sample.csv s3://$BUCKET/settlement/sample.csv"
echo "  aws --endpoint-url=$ENDPOINT sqs receive-message --queue-url $QUEUE_URL"
