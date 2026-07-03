variable "aws_region" {
  type        = string
  description = "AWS region."
  default     = "ap-northeast-1"
}

variable "environment" {
  type        = string
  description = "Environment name. Used for tagging and naming."
  default     = "prod"
}

variable "cluster_name" {
  type        = string
  description = "EKS cluster name."
}

variable "cluster_version" {
  type        = string
  description = "Kubernetes version."
  default     = "1.31"
}

variable "vpc_id" {
  type        = string
  description = "VPC ID for the EKS cluster."

  validation {
    condition     = length(var.vpc_id) > 0
    error_message = "vpc_id must not be empty."
  }
}

variable "subnet_ids" {
  type        = list(string)
  description = "Private subnet IDs for EKS nodes."

  validation {
    condition     = length(var.subnet_ids) >= 2
    error_message = "At least two subnet IDs are required for EKS multi-AZ placement."
  }
}

variable "node_instance_types" {
  type        = list(string)
  description = "EC2 instance type preference list for the default node group."
  default     = ["m5.large"]
}

variable "node_desired_size" {
  type    = number
  default = 3
}

variable "node_min_size" {
  type    = number
  default = 2
}

variable "node_max_size" {
  type    = number
  default = 10
}

variable "static_assets_bucket_name" {
  type        = string
  description = "Globally unique S3 bucket name for static assets."
}

variable "enable_bucket_versioning" {
  type    = bool
  default = true
}
