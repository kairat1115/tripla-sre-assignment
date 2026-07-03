terraform {
  required_version = ">= 1.10.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.53"
    }
  }
}

provider "aws" {
  region = var.aws_region
}

module "tfstate_backend" {
  source  = "cloudposse/tfstate-backend/aws"
  version = "~> 1.9"

  namespace = "tripla"
  name      = "terraform-state"

  force_destroy = false

  s3_state_lock_enabled = true
  dynamodb_enabled      = false
}

variable "aws_region" {
  type        = string
  description = "AWS region for the state bucket."
  default     = "ap-northeast-1"
}
