# Request to create plan.md
we are going to use standard go http library. be mindful to use https://github.com/golang-standards/project-layout to implement code layout. use https://google.github.io/styleguide/go/ google style guide for writing code. use wide log events.
I want to create an endpoint `api/aws/v1/buckets` that accepts POST method.
the endpoint accepts payload
```
{
    "payload":{
        "properties":{
            "aws-region":"eu-west-1",
            "acl":"private",
            "bucket-name": "tripla-bucket"
        }
    }
}
```
properties are extendable in the future. once the user hit the endpoint, service must create a file in storage (right now only filesystem) main.tf that contains.
Resources created should at least include:
provider
aws_s3_bucket
aws_s3_bucket_acl
Use go built-in template engine for that and include sprig to utilize additional functionality.
write a detailed plan.md document outlining how to implement this. include code snippets

---

lots of remarks and edits to plan, and ask clause with the following - I added a few notes to the document, address all the notes and update the document accordingly. don’t implement yet

---

add a detailed todo list to the plan, with all the phases and individual tasks necessary to complete the plan - don’t implement yet

---

implement it all. when you’re done with a task or phase, mark it as completed in the plan document. do not stop until all tasks and phases are completed. do not add unnecessary comments or jsdocs, do not use any or unknown types. continuously run typecheck to make sure you’re not introducing new issues. make sure to update plan document with completed task or phase before proceding to the next task or phase.
