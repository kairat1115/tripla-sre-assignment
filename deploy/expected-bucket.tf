terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

provider "aws" {
  region = "us-east-1"
}

resource "aws_s3_bucket" "test_bucket_01" {
  bucket = "test-bucket-01"
}

resource "aws_s3_bucket_acl" "test_bucket_01_acl" {
  bucket = aws_s3_bucket.test_bucket_01.id
  acl    = "private"
}
