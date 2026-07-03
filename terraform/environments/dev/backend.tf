terraform {
  backend "s3" {
    bucket       = "tripla-terraform-state" # placeholder — fill from bootstrap output: state_bucket_id
    key          = "dev/terraform.tfstate"
    region       = "ap-northeast-1"
    encrypt      = true
    use_lockfile = true
  }
}
