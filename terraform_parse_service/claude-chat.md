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

# Request to create readme

Create README.md

---

and some manual polishing

# Request to create plan2.md
this log message is useless. can we make it more meaningful

request:
curl -v localhost:8080/api/aws/v1/s3/buckets -d '{"payload":{"properties":{"aws-region":"eu-west-1","acl":"private","bucket-name":"tripla-bucket"}}'
log:
{"level":"info","ts":1782802822.3116622,"caller":"s3/bucket.go:83","msg":"unexpected EOF","service":"terraform-parse-service","method":"POST","path":"/api/aws/v1/s3/buckets","remote_addr":"[::1]:57498","status":400,"duration_ms":0}

rename request handled to something more meaningful, associated with what outcome has been achieved
request:
curl -v localhost:8080/api/aws/v1/s3/buckets -d '{"payload":{"properties":{"aws-region":"eu-west-1","acl":"private","bucket-name":"tripla-bucket"}}}'
log:
{"level":"info","ts":1782802750.3982568,"caller":"s3/bucket.go:120","msg":"request handled","service":"terraform-parse-service","method":"POST","path":"/api/aws/v1/s3/buckets","remote_addr":"[::1]:57467","status":201,"aws-region":"eu-west-1","acl":"private","bucket-name":"tripla-bucket","output_path":"output/aws/s3/tripla-bucket/main.tf","duration_ms":1}

this log shows error but its unknown what request is, thus not debugable
request:
curl -v localhost:8080/api/aws/v1/s3/buckets -d '{"payload":{"properties":{"aws-region":"eu-west-1","acl":"private","bucket-name":tripla-bucket}}}'
log:
{"level":"info","ts":1782802968.831769,"caller":"s3/bucket.go:83","msg":"invalid character 'i' in literal true (expecting 'u')","service":"terraform-parse-service","method":"POST","path":"/api/aws/v1/s3/buckets","remote_addr":"[::1]:57511","status":400,"duration_ms":0}

revamp logs to make them meaningful for the engineer to debug. write a detailed plan2.md document outlining how to implement this. include code snippets

---

some remarks in the plan and ask clause with the following - I added a few notes to the document, address all the notes and update the document accordingly. don’t implement yet

---

add a detailed todo list to the plan, with all the phases and individual tasks necessary to complete the plan - don’t implement yet

---

implement it all. when you’re done with a task or phase, mark it as completed in the plan document. do not stop until all tasks and phases are completed. do not add unnecessary comments or jsdocs, do not use any or unknown types. continuously run typecheck to make sure you’re not introducing new issues. make sure to update plan document with completed task or phase before proceding to the next task or phase.

# Request to create plan3.md
now i want to add tracing instrumentation to the code with opentelemetry. traces contain more details than logs as they will be used for deep debugging. write a detailed plan3.md document outlining how to implement this. include code snippets

---

lots of remarks and edits to plan, and ask clause with the following - I added a few notes to the document, address all the notes and update the document accordingly. don’t implement yet

---

add a detailed todo list to the plan, with all the phases and individual tasks necessary to complete the plan - don’t implement yet

---

implement it all. when you’re done with a task or phase, mark it as completed in the plan document. do not stop until all tasks and phases are completed. do not add unnecessary comments or jsdocs, do not use any or unknown types. continuously run typecheck to make sure you’re not introducing new issues. make sure to update plan document with completed task or phase before proceding to the next task or phase.
---

lots of remarks and edits to plan, and ask clause with the following - I added a few notes to the document, address all the notes and update the document accordingly. don’t implement yet

---

add a detailed todo list to the plan, with all the phases and individual tasks necessary to complete the plan - don’t implement yet

---

implement it all. when you’re done with a task or phase, mark it as completed in the plan document. do not stop until all tasks and phases are completed. do not add unnecessary comments or jsdocs, do not use any or unknown types. continuously run typecheck to make sure you’re not introducing new issues. make sure to update plan document with completed task or phase before proceding to the next task or phase.

# inject trace id to logs

inject traceid to logs
