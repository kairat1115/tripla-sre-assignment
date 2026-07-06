# research solution

read ../helm and ../terraform_parse_service in depth, understand how it works deeply, what it does and all its specificities. when that’s done, write a detailed report of your learnings and findings in research.md

# write plan.md

I want to test the solution terraform_parse_service. I need to deploy kind, install istio and as in docker compose install grafana alloy, grafana tempo, grafana loki, grafana and prometheus. i want to deploy service with helm/terraform-parse-service and use prod values. Image will be built locally and loaded to node with kind load. use sha as tag. write a detailed plan.md document outlining how to implement this. include code snippets

---

lots of reviews and requests to change plan and ask repeatedly with - I added a few notes to the document, address all the notes and update the document accordingly. don’t implement yet

---

add a detailed todo list to the plan, with all the phases and individual tasks necessary to complete the plan - don’t implement yet

---

implement it all. when you’re done with a task or phase, mark it as completed in the plan document. do not stop until all tasks and phases are completed. do not add unnecessary comments or jsdocs, do not use any or unknown types. continuously run typecheck to make sure you’re not introducing new issues. make sure to update plan document with completed task or phase before proceding to the next task or phase.

# small fixes over previous plan

i now create rbac with helm, we can remove that step. adjust values-prod in helm terraform service
