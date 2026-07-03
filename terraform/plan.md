# Terraform Refactor Plan

## Issues Found

| Severity | Location | Issue |
|---|---|---|
| Blocking | `main.tf:21` | `cluster_version = "1.25"` — EKS EOL, AWS refuses to create clusters at this version |
| Blocking | `variables.tf:12,17` | `vpc_id` and `subnet_ids` default to empty — module fails at plan without real values |
| Critical | `main.tf:37` | `acl = "public-read"` — all objects publicly readable by default |
| Critical | `main.tf` | No remote backend — state is local, diverges on second machine, no locking |
| High | `main.tf:23` | `node_groups` is the self-managed key; managed node groups require `eks_managed_node_groups` |
| High | `main.tf:4` | AWS provider pinned to `~> 4.0` — inline `acl` deprecated there, removed in v5 |
| High | `main.tf:36` | `bucket = "tripla-static-assets"` hardcoded — globally scoped, apply fails if name taken |
| Medium | `main.tf` | No S3 public access block resource — relies on account-level settings |
| Medium | `main.tf` | No EKS secrets encryption, no cluster logging |
| Medium | `outputs.tf:2` | `cluster_name` output references `module.eks.cluster_id` — semantic mismatch |
| Low | `outputs.tf:6` | `cluster_endpoint` not marked `sensitive = true` |
| Low | `main.tf:38` | Tag key `Env` on S3 vs `Environment` on EKS — inconsistent schema |
| Low | `variables.tf` | No `description` or `validation` on any variable |

---

## Target Structure

The refactor uses two upstream modules called directly from each environment. No custom modules are needed.

```
terraform/
├── README.md              # operator guide: setup, per-env workflow, adding envs, state ops
├── bootstrap/
│   ├── main.tf            # cloudposse/terraform-aws-tfstate-backend — creates S3
│   ├── outputs.tf         # emits state_bucket_id for use in each environment's backend.tf
│   ├── terraform.tfvars
│   └── init-bootstrap.sh  # provisions S3 backend and migrates bootstrap local state to it
└── environments/
    ├── dev/
    │   ├── backend.tf     # S3 backend pointing to dev state key
    │   ├── main.tf        # terraform-aws-modules/eks/aws + terraform-aws-modules/s3-bucket/aws
    │   ├── variables.tf
    │   ├── outputs.tf
    │   └── terraform.tfvars
    ├── staging/
    │   └── ...            # same shape as dev
    └── prod/
        └── ...            # same shape as dev
```

`modules/` is absent entirely. Both `terraform-aws-modules/eks/aws` and `terraform-aws-modules/s3-bucket/aws` already expose the variables we need with correct secure defaults:

- `terraform-aws-modules/s3-bucket/aws` (`~> 5.14`): public access block all-true by default, `versioning` map, `server_side_encryption_configuration`, `attach_policy` + `policy` for optional resource policy — covers everything our custom S3 module did
- `terraform-aws-modules/eks/aws` (`~> 21.0`): KMS, IRSA, logging already handled

Each environment directory is independently `terraform init`-able and maintains isolated state. A `terraform destroy` in `dev/` cannot touch `prod/`.

---

## Step 1 — Bootstrap Backend (prerequisite, run once)

Backend infrastructure is managed by the `cloudposse/terraform-aws-tfstate-backend` module. This is a dedicated Terraform root that runs with **local state** — its only job is to create the shared S3 bucket that all environments use for remote state. No DynamoDB table is provisioned; locking is handled by S3-native `use_lockfile`.

**`bootstrap/main.tf`**
```hcl
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

  # Do not let terraform destroy wipe the state bucket — require explicit override.
  force_destroy = false

  # S3-native locking (requires Terraform >= 1.10). Since Terraform 1.15.7 is used,
  # DynamoDB is not needed — use_lockfile handles locking directly on the state object.
  s3_state_lock_enabled = true
  dynamodb_enabled      = false
}

variable "aws_region" {
  type    = string
  default = "ap-northeast-1"
}
```

**`bootstrap/outputs.tf`**
```hcl
output "state_bucket_id" {
  value       = module.tfstate_backend.s3_bucket_id
  description = "S3 bucket name to use in each environment's backend.tf."
}
```

The full provisioning and state migration sequence is handled by `bootstrap/init-bootstrap.sh` (see Step 5). After that script runs, bootstrap state lives at `s3://<bucket>/bootstrap/terraform.tfstate` — no local state file to commit or store.

---

## Step 2 — S3 Configuration

`terraform-aws-modules/s3-bucket/aws` (`~> 5.14`) is called directly from each environment — no internal wrapper module. It already manages public access block, versioning, SSE, and optional bucket policy in a single module call.

Key variables (confirmed from module source):

| Variable | Default | Notes |
|---|---|---|
| `bucket` | `null` | Required — supply the bucket name |
| `versioning` | `{}` | Pass `{ enabled = true }` to enable versioning |
| `server_side_encryption_configuration` | `{}` | Pass a rule map to configure SSE |
| `block_public_acls` | `true` | Secure by default |
| `ignore_public_acls` | `true` | Secure by default |
| `block_public_policy` | `true` | Set to `false` for CloudFront OAC |
| `restrict_public_buckets` | `true` | Set to `false` for CloudFront OAC |
| `attach_policy` | `false` | Set to `true` to apply a custom policy |
| `policy` | `null` | JSON policy document string |

**Public access guide** — override in `terraform.tfvars`:

| Use case | `block_public_policy` | `restrict_public_buckets` | `attach_policy` |
|---|---|---|---|
| Fully private (default) | `true` | `true` | `false` |
| CloudFront OAC origin | `false` | `false` | `true` (attach OAC policy) |

Module outputs used in environment `outputs.tf`: `s3_bucket_id`, `s3_bucket_arn`, `s3_bucket_bucket_regional_domain_name`.

The S3 module call lives in `environments/dev/main.tf` alongside the EKS call (see Step 3).

---

## Step 3 — EKS Configuration

`terraform-aws-modules/eks/aws` is called directly from each environment — no internal wrapper module. Argument names below are as they exist in the upstream module (v21+).

Key argument renames from the original broken config (confirmed from module source):
- `cluster_name` → `name`
- `cluster_version` → `kubernetes_version`
- `cluster_enabled_log_types` → `enabled_log_types`
- `cluster_encryption_config` → `encryption_config`

Notable module defaults that require no explicit configuration:
- `create_kms_key = true` — module creates and wires up its own KMS key for secrets encryption
- `enable_irsa = true` — OIDC provider is created automatically
- `enabled_log_types` defaults to `["audit", "api", "authenticator"]` — override to add `controllerManager` and `scheduler`

The EKS block lives in `environments/dev/main.tf` alongside the S3 module call (see Step 4).

---

## Step 4 — Environment Configuration

Each environment directory is self-contained: its own backend config, module calls, and variable values. The `dev` layout is shown in full; `staging` and `prod` follow the same shape with different `terraform.tfvars`.

### Provider + Terraform version

**`environments/dev/main.tf`**
```hcl
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

  default_tags {
    tags = local.common_tags
  }
}

locals {
  common_tags = {
    Environment = var.environment
    ManagedBy   = "terraform"
    Project     = "tripla"
  }
}

module "eks" {
  source  = "terraform-aws-modules/eks/aws"
  version = "~> 21.0"  # minimum 21.24

  name               = var.cluster_name
  kubernetes_version = var.cluster_version
  vpc_id             = var.vpc_id
  subnet_ids         = var.subnet_ids

  enabled_log_types = ["api", "audit", "authenticator", "controllerManager", "scheduler"]

  eks_managed_node_groups = {
    default = {
      instance_types = var.node_instance_types
      desired_size   = var.node_desired_size
      min_size       = var.node_min_size
      max_size       = var.node_max_size
    }
  }

  tags = local.common_tags
}

module "static_assets" {
  source  = "terraform-aws-modules/s3-bucket/aws"
  version = "~> 5.14"

  bucket = var.static_assets_bucket_name
  tags   = local.common_tags

  versioning = {
    enabled = var.enable_bucket_versioning
  }

  server_side_encryption_configuration = {
    rule = {
      apply_server_side_encryption_by_default = {
        sse_algorithm = "AES256"
      }
    }
  }

  # All four public access block flags default to true in the module — secure by default.
  # Override block_public_policy and restrict_public_buckets to false,
  # and set attach_policy = true with a policy document for CloudFront OAC.
}
```

### Backend

**`environments/dev/backend.tf`**
```hcl
terraform {
  backend "s3" {
    bucket       = "tripla-shared-terraform-state"  # from bootstrap output: state_bucket_id
    key          = "dev/terraform.tfstate"
    region       = "ap-northeast-1"
    encrypt      = true
    use_lockfile = true  # S3-native locking; requires Terraform >= 1.10, no DynamoDB needed
  }
}
```

`staging` and `prod` use the same bucket with different `key` values: `staging/terraform.tfstate`, `prod/terraform.tfstate`.

### Variables

**`environments/dev/variables.tf`**
```hcl
variable "aws_region" {
  type        = string
  description = "AWS region."
  default     = "ap-northeast-1"
}

variable "environment" {
  type        = string
  description = "Environment name. Used for tagging and naming."
  default     = "dev"
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
}

variable "subnet_ids" {
  type        = list(string)
  description = "Private subnet IDs for EKS nodes."
}

variable "node_instance_types" {
  type        = list(string)
  description = "EC2 instance type preference list for the default node group."
  default     = ["t3.medium"]
}

variable "node_desired_size" {
  type    = number
  default = 2
}

variable "node_min_size" {
  type    = number
  default = 1
}

variable "node_max_size" {
  type    = number
  default = 4
}

variable "static_assets_bucket_name" {
  type        = string
  description = "Globally unique S3 bucket name for static assets."
}

variable "enable_bucket_versioning" {
  type    = bool
  default = true
}
```

### tfvars per environment

**`environments/dev/terraform.tfvars`**
```hcl
environment               = "dev"
cluster_name              = "tripla-dev-eks"
vpc_id                    = "vpc-0abc123"
subnet_ids                = ["subnet-0aaa111", "subnet-0bbb222"]
node_instance_types       = ["t3.medium", "t3.large"]
node_desired_size         = 2
node_min_size             = 1
node_max_size             = 4
static_assets_bucket_name = "tripla-static-assets-dev"
enable_bucket_versioning  = true
```

**`environments/prod/terraform.tfvars`**
```hcl
environment               = "prod"
cluster_name              = "tripla-prod-eks"
vpc_id                    = "vpc-0xyz789"
subnet_ids                = ["subnet-0ccc333", "subnet-0ddd444", "subnet-0eee555"]
node_instance_types       = ["m5.large", "m5.xlarge", "m5a.large"]
node_desired_size         = 3
node_min_size             = 2
node_max_size             = 10
static_assets_bucket_name = "tripla-static-assets-prod"
enable_bucket_versioning  = true
```

### Outputs

**`environments/dev/outputs.tf`**
```hcl
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
```

---

## Step 5 — Bootstrap Init Script

`init-bootstrap.sh` handles the full one-time bootstrap sequence: provision the S3 bucket, write `backend.tf` with the bucket name, then migrate the bootstrap local state into that bucket via `terraform init -migrate-state`. After this runs, `bootstrap/terraform.tfstate` no longer exists locally — it lives in S3.

Environment roots (`environments/dev`, etc.) don't need migration. When `terraform init` is run in an environment directory for the first time, the S3 bucket already exists and the state file is simply created there from scratch on the first `apply`.

**`bootstrap/init-bootstrap.sh`**
```bash
#!/usr/bin/env bash
# Run once to provision the remote state bucket and migrate bootstrap state into it.
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
```

Run once:
```bash
bash bootstrap/init-bootstrap.sh
```

---

## Step 6 — Deployment Sequence

```
1. Paste the bucket name from bootstrap output into each environments/*/backend.tf bucket field,
   then run the bootstrap script (provisions bucket + migrates bootstrap state to S3):
   bash bootstrap/init-bootstrap.sh

2. cd environments/dev
   terraform init        # bucket already exists; creates dev/terraform.tfstate in S3 on first apply
   terraform plan -var-file=terraform.tfvars
   terraform apply -var-file=terraform.tfvars

3. Repeat for staging, then prod.
```

---

## Step 7 — README

A `README.md` will be written at `terraform/README.md` covering:

- **Prerequisites** — required tools and versions (Terraform >= 1.10, AWS CLI, IAM permissions needed)
- **First-time setup** — how to run `init-bootstrap.sh` and update `backend.tf` files
- **Per-environment workflow** — `terraform init` / `plan` / `apply` with `terraform.tfvars`
- **Adding a new environment** — copy `dev/` structure, set a new `key` in `backend.tf`, fill `terraform.tfvars`
- **Destroying an environment** — `terraform destroy` scope, what is and isn't affected
- **State operations** — how to read outputs, how to import existing resources
- **Module versions** — which upstream modules are pinned and where to check for updates

---

## TODO

### Phase 0 — Delete existing files
- [x] Delete `terraform/main.tf`
- [x] Delete `terraform/variables.tf`
- [x] Delete `terraform/outputs.tf`
- [x] Keep `terraform/research.md`

### Phase 1 — Bootstrap
- [x] Create `terraform/bootstrap/main.tf` with `cloudposse/tfstate-backend/aws ~> 1.9`, `namespace = "tripla"`, `name = "terraform-state"`, `s3_state_lock_enabled = true`, `dynamodb_enabled = false`, `force_destroy = false`, AWS provider `~> 6.53`, `required_version >= 1.10.0`
- [x] Create `terraform/bootstrap/outputs.tf` with `state_bucket_id` output referencing `module.tfstate_backend.s3_bucket_id`
- [x] Create `terraform/bootstrap/terraform.tfvars` with `aws_region = "ap-northeast-1"`
- [x] Create `terraform/bootstrap/init-bootstrap.sh` — `terraform init`, `terraform apply -auto-approve`, capture bucket output, write `backend.tf`, run `terraform init -migrate-state -force-copy`, remove local state files
- [x] Make `init-bootstrap.sh` executable (`chmod +x`)

### Phase 2 — Environment: dev
- [x] Create `terraform/environments/dev/backend.tf` — S3 backend with `key = "dev/terraform.tfstate"`, `encrypt = true`, `use_lockfile = true`; bucket field left as placeholder to be filled from bootstrap output
- [x] Create `terraform/environments/dev/main.tf`:
  - [x] `terraform` block: `required_version >= 1.10.0`, AWS provider `~> 6.53`
  - [x] `provider "aws"` with `region = var.aws_region` and `default_tags { tags = local.common_tags }`
  - [x] `locals` block: `common_tags` map with `Environment`, `ManagedBy = "terraform"`, `Project = "tripla"`
  - [x] `module "eks"` calling `terraform-aws-modules/eks/aws ~> 21.0` with `name`, `kubernetes_version`, `vpc_id`, `subnet_ids`, `enabled_log_types`, `eks_managed_node_groups` (instance_types, desired/min/max size), `tags`
  - [x] `module "static_assets"` calling `terraform-aws-modules/s3-bucket/aws ~> 5.14` with `bucket`, `tags`, `versioning`, `server_side_encryption_configuration`; public access block flags at defaults (all true)
- [x] Create `terraform/environments/dev/variables.tf` — declare all variables: `aws_region`, `environment`, `cluster_name`, `cluster_version`, `vpc_id`, `subnet_ids`, `node_instance_types`, `node_desired_size`, `node_min_size`, `node_max_size`, `static_assets_bucket_name`, `enable_bucket_versioning`; add `description` and `validation` on `vpc_id` (non-empty) and `subnet_ids` (length >= 2)
- [x] Create `terraform/environments/dev/terraform.tfvars` — fill with dev values: `cluster_name = "tripla-dev-eks"`, placeholder VPC/subnet IDs, `node_instance_types = ["t3.medium", "t3.large"]`, sizing 2/1/4, bucket name `tripla-static-assets-dev`
- [x] Create `terraform/environments/dev/outputs.tf` — `cluster_name`, `cluster_endpoint` (sensitive), `oidc_provider_arn`, `static_assets_bucket_id` (`module.static_assets.s3_bucket_id`), `static_assets_bucket_arn` (`module.static_assets.s3_bucket_arn`), `static_assets_bucket_domain_name` (`module.static_assets.s3_bucket_bucket_regional_domain_name`)

### Phase 3 — Environment: staging
- [x] Copy `environments/dev/` structure to `environments/staging/`
- [x] Update `backend.tf`: `key = "staging/terraform.tfstate"`
- [x] Update `terraform.tfvars`: staging-appropriate values (cluster name, VPC/subnets, instance types, bucket name)

### Phase 4 — Environment: prod
- [x] Copy `environments/dev/` structure to `environments/prod/`
- [x] Update `backend.tf`: `key = "prod/terraform.tfstate"`
- [x] Update `terraform.tfvars`: prod values — `cluster_name = "tripla-prod-eks"`, `node_instance_types = ["m5.large", "m5.xlarge", "m5a.large"]`, sizing 3/2/10, bucket name `tripla-static-assets-prod`

### Phase 5 — README
- [x] Create `terraform/README.md` covering:
  - [x] Prerequisites section: Terraform >= 1.10, AWS CLI, required IAM permissions (S3, DynamoDB, EKS, KMS, IAM)
  - [x] First-time setup section: run `init-bootstrap.sh`, copy bucket name into each `backend.tf`
  - [x] Per-environment workflow section: `terraform init` / `plan -var-file=terraform.tfvars` / `apply -var-file=terraform.tfvars`
  - [x] Adding a new environment section: directory copy, `backend.tf` key update, `terraform.tfvars` fill
  - [x] Destroying an environment section: scope of `terraform destroy`, what survives (bootstrap bucket, other envs)
  - [x] State operations section: `terraform output`, `terraform state list`, `terraform import`
  - [x] Module versions section: pinned versions table, upgrade procedure

### Phase 6 — Validation
- [x] Run `terraform fmt -recursive` across all new files
- [x] Run `terraform validate` in `bootstrap/`
- [x] Run `terraform validate` in `environments/dev/` (uses `-backend=false` since no live backend)
- [x] Confirm no stale references to deleted files remain

---

## What Was Not Changed and Why

- **VPC and subnets are not created here.** Network infrastructure (VPC, subnets, NAT gateways, route tables) should live in a separate Terraform root or be pre-provisioned. Coupling EKS to VPC in the same root ties their lifecycles together — destroying one destroys the other.
- **No IAM users or roles for CI/CD.** Authentication to AWS from pipelines should use OIDC federation (GitHub Actions, for example), not static IAM keys. Out of scope for this config but a required complement to a production deployment.
- **No CloudFront added.** If the S3 bucket needs to serve public content, a CloudFront distribution with Origin Access Control is the correct pattern. That belongs in a separate `cdn` module and was not present in the original config.
