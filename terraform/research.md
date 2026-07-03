# Terraform Folder Research

## Structure

Three files, no modules, no backend configuration, no subdirectories.

```
terraform/
├── main.tf       (41 lines)
├── variables.tf  (24 lines)
└── outputs.tf    (7 lines)
```

---

## What It Does

Provisions two AWS resources:

1. **EKS cluster** — via the public `terraform-aws-modules/eks/aws` module (version `19.0.0`)
2. **S3 bucket** — a single `aws_s3_bucket` resource named `tripla-static-assets`

Default region is `ap-northeast-1` (Tokyo).

---

## Provider

```hcl
terraform {
  required_version = ">= 1.0.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 4.0"
    }
  }
}
```

- AWS provider pinned to `~> 4.0` (4.x only). The current AWS provider is 5.x. This blocks use of resources and arguments added in v5.
- No backend block — state is local by default. Any `terraform apply` run on a different machine diverges state immediately.

---

## Variables

| Name | Type | Default | Notes |
|---|---|---|---|
| `aws_region` | string | `"ap-northeast-1"` | Parameterized correctly |
| `cluster_name` | string | `"tripla-messy-eks"` | Default name includes "messy" — suggests this is intentionally broken/demo config |
| `vpc_id` | string | `""` | Empty default — `module.eks` will fail at plan time without a real value |
| `subnet_ids` | list(string) | `[]` | Empty default — same failure as above |
| `environment` | string | `"dev"` | Used only for tagging |

No `description` fields on any variable. No `validation` blocks. `vpc_id` and `subnet_ids` having empty defaults makes the config non-runnable out of the box — `terraform plan` will error because EKS requires both.

---

## EKS Module

```hcl
module "eks" {
  source  = "terraform-aws-modules/eks/aws"
  version = "19.0.0"

  cluster_name    = var.cluster_name
  cluster_version = "1.25"
  vpc_id          = var.vpc_id
  subnet_ids      = var.subnet_ids
  node_groups = {
    default = {
      desired_capacity = 2
      instance_type    = "t3.medium"
    }
  }

  tags = {
    Environment = var.environment
  }
}
```

**Issues:**

- `cluster_version = "1.25"` is hardcoded. Kubernetes 1.25 reached end-of-life in October 2023. EKS no longer supports creating clusters at this version — this will fail at apply time.
- `desired_capacity`, `instance_type` are hardcoded with no corresponding variables. Cannot be changed without editing the file.
- No `min_size` / `max_size` on the node group — no autoscaling boundary defined.
- The `node_groups` argument used here is the legacy key for this module version. Module `19.0.0` uses `eks_managed_node_groups` as the primary argument. `node_groups` maps to self-managed node groups with a different schema. The config will either silently produce a different result or error depending on the exact module internals at v19.0.0.
- No IAM role configuration, no OIDC provider, no addon configuration, no logging, no encryption for secrets at rest.
- Module version `19.0.0` is pinned exactly (no `~>`) which prevents patch-level bug fixes from being picked up.

---

## S3 Bucket

```hcl
resource "aws_s3_bucket" "static_assets" {
  bucket = "tripla-static-assets"
  acl    = "public-read"
  tags = {
    Env = var.environment
  }
}
```

**Issues:**

- `acl = "public-read"` makes every object uploaded to this bucket world-readable by default. This is a critical misconfiguration for anything other than a deliberately public CDN origin.
- The `acl` argument is deprecated in AWS provider `~> 4.0` and removed in v5. The current approach requires a separate `aws_s3_bucket_acl` resource plus `aws_s3_bucket_ownership_controls`. Using inline `acl` on the bucket resource is a legacy pattern that AWS itself has deprecated.
- Bucket name `"tripla-static-assets"` is hardcoded and globally unique in S3. If this name is already taken by any AWS account, the apply fails. No variable, no prefix, no suffix.
- No `aws_s3_bucket_public_access_block` resource — there is no explicit block on public access, which means account-level public access block settings can override or be overridden unexpectedly.
- No versioning, no lifecycle rules, no server-side encryption configuration.
- Tag key is `Env` here but `Environment` on the EKS module — inconsistent tagging schema.

---

## Outputs

```hcl
output "cluster_name" {
  value = module.eks.cluster_id
}

output "cluster_endpoint" {
  value = module.eks.cluster_endpoint
}
```

- Output is named `cluster_name` but references `module.eks.cluster_id`. These are different values — the cluster ID is not the same as the cluster name in all cases. This is a semantic mismatch.
- No `sensitive = true` on `cluster_endpoint` — endpoint URL will appear in plaintext in state and CLI output.
- No output for the S3 bucket name, ARN, or domain — the bucket is unobservable from outputs.

---

## Summary of Issues by Severity

**Blocking (apply will fail):**
- `cluster_version = "1.25"` — EKS no longer accepts this version
- `vpc_id` and `subnet_ids` default to empty — EKS module requires real values

**Critical (security):**
- `acl = "public-read"` on the S3 bucket — all objects publicly readable
- No S3 public access block resource

**Degraded functionality:**
- `node_groups` key is likely wrong for module v19.0.0; should be `eks_managed_node_groups`
- Inline `acl` argument deprecated in the pinned provider version
- `cluster_name` output references `cluster_id` — semantic mismatch

**Operational gaps:**
- No remote backend — state is local only
- No variable descriptions or validation
- No autoscaling bounds on node group
- No encryption configuration (EKS secrets, S3 at rest)
- No cluster logging enabled
- AWS provider pinned to v4 — blocks v5 features and security fixes
- Inconsistent tag keys (`Env` vs `Environment`) across resources
