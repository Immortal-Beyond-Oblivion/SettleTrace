locals {
  enabled = var.create_demo_resources && var.landing_bucket_name != ""
  tags = {
    Project     = var.project_name
    Environment = var.environment
    ManagedBy   = "terraform"
    CostGuard   = "teardown-after-demo"
  }
}

resource "aws_s3_bucket" "landing" {
  count  = local.enabled ? 1 : 0
  bucket = var.landing_bucket_name
  tags   = local.tags
}

resource "aws_s3_bucket_public_access_block" "landing" {
  count                   = local.enabled ? 1 : 0
  bucket                  = aws_s3_bucket.landing[0].id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_versioning" "landing" {
  count  = local.enabled ? 1 : 0
  bucket = aws_s3_bucket.landing[0].id
  versioning_configuration { status = "Enabled" }
}

resource "aws_sqs_queue" "ingestion_dlq" {
  count                     = local.enabled ? 1 : 0
  name                      = "${var.project_name}-${var.environment}-ingestion-dlq"
  message_retention_seconds = 1209600
  tags                      = local.tags
}

resource "aws_sqs_queue" "ingestion" {
  count                      = local.enabled ? 1 : 0
  name                       = "${var.project_name}-${var.environment}-ingestion"
  visibility_timeout_seconds = 300
  redrive_policy             = jsonencode({ deadLetterTargetArn = aws_sqs_queue.ingestion_dlq[0].arn, maxReceiveCount = 3 })
  tags                       = local.tags
}

resource "aws_cloudwatch_log_group" "application" {
  count             = local.enabled ? 1 : 0
  name              = "/${var.project_name}/${var.environment}"
  retention_in_days = 14
  tags              = local.tags
}

resource "aws_ecr_repository" "services" {
  for_each             = local.enabled ? toset(["api", "ingestion-worker", "matching-engine", "fuzzy-ranker", "legacy-adapter"]) : toset([])
  name                 = "${var.project_name}-${var.environment}-${each.value}"
  image_tag_mutability = "IMMUTABLE"
  image_scanning_configuration { scan_on_push = true }
  tags = local.tags
}

resource "aws_secretsmanager_secret" "webhook" {
  count                   = local.enabled ? 1 : 0
  name                    = "${var.project_name}/${var.environment}/webhook-hmac"
  recovery_window_in_days = 7
  tags                    = local.tags
}

output "cost_guard_message" {
  value = local.enabled ? "Demo resources enabled. Destroy them after the demo." : "No demo resources will be created until create_demo_resources and landing_bucket_name are set."
}
