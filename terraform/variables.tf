variable "project_name" {
  description = "Short lowercase prefix used for AWS resource names."
  type        = string
  default     = "settletrace"
}

variable "environment" {
  description = "Deployment environment name."
  type        = string
  default     = "demo"
}

variable "landing_bucket_name" {
  description = "Globally unique S3 bucket name for synthetic landing files."
  type        = string
  default     = ""
}

variable "create_demo_resources" {
  description = "Explicit cost guard that must be true before Terraform creates demo resources."
  type        = bool
  default     = false
}
