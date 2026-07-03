# Part 1 (API Service)
## Request
Describe how you implemented the Terraform-Parse service. Include the framework/language you chose, how the API works, and how it translates the payload into Terraform code.

## Response
The code was written completely with Claude AI where I have described the layout, coding style, frameworks and preferrences how to use what. You can observe chats in the [Claude chat](./terraform_parse_service/claude-chat.md) and plans within service folder.

The language of choice is Golang as I want it to be close to the language of the terraform which is also Golang that allows us to use terraform libraries to produce the same output as terraform expects if we would need it in the future.

The I have not used http framework like fiber because standard http network stack is proven to be used in many production grade projects like kubernetes, istio, grafana, prometheus.
Though I have used Uber's projects for config for convenient configuration ingestion via YAML and logging (zap) as the buffered output of standard slog is much slower and consumes more memory than zap if the service will receive huge amount of traffic. Check out the [Performance](https://github.com/uber-go/zap#performance).

For the metrics, I have used standard prometheus library and for the tracing I have used opentelemetry.

The api has the following pattern for the endpoints - `/api/{provider}/v1/{service}/{resource}`

This allows us to enhance service to support multiple resources per service and multiple providers. Worth noting that versioning of api goes after provider to not force clients to change api version if breaking change has happened on a different provider.

Providers could be following - aws, azure, gcp, openstack, and more ...
Services could be, in our case aws - s3, ec2, ses, sns, sqs, rds, eks, etc...
Resources could be, in our case aws s3 - buckets. maybe something else? Some ideas to copy resource names from [terraform provider](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket)

The api receives JSON object, parses it, checks if all parameters are defined, feeds parameters to terraform service that renders terraform template main.tf with the supplied parameters with gotemplate and sprig templating functions. The template path and output path is configurable inside config so you have the flexibility of defining templates however you wish without recompiling the program and output per-provider wherever you want.

The storage is chosen as filesystem as of the moment, but we can support many storages as it is abstracted with interface where implementation can be different per provider.

Total cost of developing the service:
Session
  Total cost:            $46.40
  Total duration (API):  2h 49m 45s
  Total duration (wall): 1d 22h 55m
  Total code changes:    5231 lines added, 907 lines removed
  Usage by model:
     claude-sonnet-4-6:  358.2k input, 675.2k output, 79.6m cache read, 3.0m cache write ($46.34)
      claude-haiku-4-5:  3.0k input, 3.7k output, 141.2k cache read, 15.3k cache write ($0.0549)


# Part 2 (Terraform)
## Request
Describe the issues you found and how you approached improving them. Mention anything you think could still be enhanced.

## Response

The usage of third-party EKS module is fine and reduces operational overhead to write your own module by trading flexibility with a well-curated modules that is used by many. There are some stale values but overall is ok. The bucket is created with ACL public which might be intentional but is not recommended nowadays as it exposes bucket to the internet with full read permissions.
```terraform
resource "aws_s3_bucket" "static_assets" {
  bucket = "tripla-static-assets"
  acl    = "public-read" <--- here
  tags = {
    Env = var.environment
  }
}
```

Not a big issue is that values are hardcoded and better to provide via tfvars that is easier to review because all configurable values are in the one place.

One issue that was discovered is missing backend file that signals that state file is saved locally which disallows other engineers to operate the infrastructure. I have added s3 backend and with new s3 features we can use object locking without need of dynamo db.

The default design is not flexible for multiple environments where operator cannot introduce new changes to test easily within the existing structure and some values are fixed. The better approach is to split files per environment directory with each own state from terraform 1.10+. Here is the details of [state locking](https://developer.hashicorp.com/terraform/language/backend/s3#state-locking).

I have chosen the approach of common modules and per environment the engineer choses which modules to use. This completely isolates environments and lets user to experiment and control when to upgrade what and which additional features to turn on.

Some values are put in tfvars and could be potentially fetched from remote state, such as vpc id and subnets.

Total cost of refactoring the terraform code:
  Total cost:            $7.75
  Total duration (API):  25m 49s
  Total duration (wall): 1d 21h 49m
  Total code changes:    2076 lines added, 588 lines removed
  Usage by model:
     claude-sonnet-4-6:  97.3k input, 99.0k output, 12.9m cache read, 549.5k cache write ($7.72)
      claude-haiku-4-5:  405 input, 3.5k output, 83.6k cache read, 6.5k cache write ($0.0346)

# Part 3 (Helm)
## Request
Explain the problems you encountered with the chart, how you addressed them, and how you validated your changes.

## Response

# Part 4 (System Behavior)
## Request
Share your thoughts on how this setup might behave under load or in failure scenarios, and what strategies could make it more resilient in the long term.

## Response

# Part 5 (Approach & Tools)
## Request
Outline the approach you took to complete the task, including any resources, tools, or methods that supported your work.

## Response
