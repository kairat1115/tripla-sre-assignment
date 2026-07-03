output "state_bucket_id" {
  value       = module.tfstate_backend.s3_bucket_id
  description = "S3 bucket name to use in each environment's backend.tf."
}
