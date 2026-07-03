output "cluster_name" {
  value       = module.eks.cluster_name
  description = "EKS cluster name."
}

output "cluster_endpoint" {
  value       = module.eks.cluster_endpoint
  description = "EKS API server endpoint."
  sensitive   = true
}

output "oidc_provider_arn" {
  value       = module.eks.oidc_provider_arn
  description = "OIDC provider ARN for IRSA."
}

output "static_assets_bucket_id" {
  value       = module.static_assets.s3_bucket_id
  description = "Static assets bucket name."
}

output "static_assets_bucket_arn" {
  value       = module.static_assets.s3_bucket_arn
  description = "Static assets bucket ARN."
}

output "static_assets_bucket_domain_name" {
  value       = module.static_assets.s3_bucket_bucket_regional_domain_name
  description = "Regional domain name for the static assets bucket."
}
