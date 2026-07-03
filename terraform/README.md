# Terraform

Multi-environment Terraform configuration for the Tripla platform. Manages EKS clusters and S3 static asset buckets across `dev`, `staging`, and `prod`.

## Structure

```
terraform/
├── bootstrap/
│   ├── main.tf             # cloudposse/tfstate-backend/aws — provisions shared S3 state bucket
│   ├── outputs.tf
│   ├── terraform.tfvars
│   └── init-bootstrap.sh   # one-time provisioning script
└── environments/
    ├── dev/
    │   ├── backend.tf
    │   ├── main.tf
    │   ├── variables.tf
    │   ├── outputs.tf
    │   └── terraform.tfvars
    ├── staging/
    │   └── ...
    └── prod/
        └── ...
```

Each environment directory is an independent Terraform root with isolated state. A `terraform destroy` in `dev/` has no effect on `staging/` or `prod/`.

---

## Prerequisites

- Terraform >= 1.10.0
- AWS CLI configured with credentials that have the permissions listed below
- The target VPC and subnets must already exist (networking is managed separately)

**Required IAM permissions:**

| Service | Actions |
|---|---|
| S3 | `s3:CreateBucket`, `s3:PutObject`, `s3:GetObject`, `s3:ListBucket`, `s3:PutBucketPolicy`, `s3:PutBucketVersioning`, `s3:PutEncryptionConfiguration`, `s3:PutPublicAccessBlock` |
| EKS | `eks:CreateCluster`, `eks:DescribeCluster`, `eks:UpdateClusterConfig`, `eks:CreateNodegroup`, `eks:DeleteCluster`, full node group lifecycle |
| IAM | `iam:CreateRole`, `iam:AttachRolePolicy`, `iam:CreateOpenIDConnectProvider` (for IRSA) |
| KMS | `kms:CreateKey`, `kms:CreateAlias`, `kms:DescribeKey`, `kms:PutKeyPolicy` |
| EC2 | `ec2:DescribeVpcs`, `ec2:DescribeSubnets`, `ec2:DescribeSecurityGroups`, and autoscaling group permissions for node groups |

---

## First-time Setup

Run once before any environment can be initialized. This provisions the shared S3 bucket used for all remote state.

```bash
cd terraform/
bash bootstrap/init-bootstrap.sh
```

The script:
1. Runs `terraform init` and `terraform apply` with local state in `bootstrap/`
2. Captures the bucket name from `terraform output -raw state_bucket_id`
3. Writes `bootstrap/backend.tf` pointing at that bucket
4. Runs `terraform init -migrate-state -force-copy` to move bootstrap state into S3
5. Deletes the local `terraform.tfstate` file

After the script completes, copy the bucket name into each `environments/*/backend.tf`:

```hcl
terraform {
  backend "s3" {
    bucket = "<bucket-name-from-bootstrap-output>"  # <-- replace this
    ...
  }
}
```

---

## Per-environment Workflow

```bash
cd environments/dev

terraform init
terraform plan -var-file=terraform.tfvars
terraform apply -var-file=terraform.tfvars
```

Repeat for `staging` and `prod` in order. Each environment init creates its state file in S3 on the first apply — no migration step needed.

---

## Adding a New Environment

1. Copy an existing environment directory:
   ```bash
   cp -r environments/dev environments/newenv
   ```

2. Set a unique state key in `environments/newenv/backend.tf`:
   ```hcl
   key = "newenv/terraform.tfstate"
   ```

3. Update `environments/newenv/variables.tf` — change the `default` for `environment` if desired.

4. Fill `environments/newenv/terraform.tfvars` with the correct VPC, subnets, cluster name, instance types, and bucket name.

5. Run `terraform init && terraform apply -var-file=terraform.tfvars` from the new directory.

---

## Destroying an Environment

```bash
cd environments/dev
terraform destroy -var-file=terraform.tfvars
```

This destroys only the EKS cluster and S3 bucket in `dev`. It does not affect:
- The bootstrap S3 state bucket (`force_destroy = false` on the bootstrap module — requires a manual override)
- Any other environment's resources or state

---

## State Operations

```bash
# List all resources in an environment
terraform state list

# Show details for a specific resource
terraform state show module.eks.aws_eks_cluster.this[0]

# Read outputs
terraform output
terraform output -raw cluster_endpoint  # prints without quotes (sensitive — requires -raw)

# Import an existing resource
terraform import module.static_assets.aws_s3_bucket.this tripla-static-assets-dev
```

---

## Module Versions

| Module | Pinned version | Registry |
|---|---|---|
| `cloudposse/tfstate-backend/aws` | `~> 1.9` | https://registry.terraform.io/modules/cloudposse/tfstate-backend/aws |
| `terraform-aws-modules/eks/aws` | `~> 21.0` (minimum 21.24) | https://registry.terraform.io/modules/terraform-aws-modules/eks/aws |
| `terraform-aws-modules/s3-bucket/aws` | `~> 5.14` | https://registry.terraform.io/modules/terraform-aws-modules/s3-bucket/aws |
| `hashicorp/aws` provider | `~> 6.53` | https://registry.terraform.io/providers/hashicorp/aws |

To upgrade a module: update the `version` constraint, run `terraform init -upgrade`, review the changelog for breaking argument changes, then `terraform plan`.
