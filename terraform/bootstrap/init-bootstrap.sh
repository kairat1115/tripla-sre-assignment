#!/usr/bin/env bash
set -euo pipefail

REGION="${AWS_REGION:-ap-northeast-1}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

echo "Step 1: init and apply with local state"
terraform init
terraform apply -auto-approve

BUCKET=$(terraform output -raw state_bucket_id)
echo "Bucket: $BUCKET"

echo "Step 2: write backend.tf pointing at the bucket"
cat > backend.tf <<HCL
terraform {
  backend "s3" {
    bucket       = "$BUCKET"
    key          = "bootstrap/terraform.tfstate"
    region       = "$REGION"
    encrypt      = true
    use_lockfile = true
  }
}
HCL

echo "Step 3: migrate local state to S3"
terraform init -migrate-state -force-copy

echo "Step 4: remove local state file"
rm -f terraform.tfstate terraform.tfstate.backup

echo "Done. Bootstrap state is now stored at s3://$BUCKET/bootstrap/terraform.tfstate"
