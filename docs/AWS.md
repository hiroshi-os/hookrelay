# hookrelay on AWS (dev)

```text
Internet → ALB → ECS/Fargate API (WORKERS=0) → RDS PostgreSQL
                              └→ ECS/Fargate workers (WORKERS=4) → customer HTTPS endpoints
CloudWatch logs/alarms · Secrets Manager · private subnets · least-privilege task execution IAM
```

Semantics stay **at-least-once**. Do not claim exactly-once delivery.

## Cost warning

This stack creates a NAT gateway, ALB, RDS (`db.t4g.micro`), and Fargate tasks. Expect tens of USD/month if left up. Set `monthly_budget_usd` and `alert_email`. **Tear down after capturing evidence.**

## Prerequisites

- Terraform >= 1.5, AWS CLI, Docker
- AWS account + credentials for `ap-south-1` (or set `aws_region`)
- GitHub OIDC role for CI (see `.github/workflows/aws.yml`)

## Deploy sequence

1. Local still works:

```bash
docker compose up --build -d
curl -sf http://127.0.0.1:8080/health
```

2. Build and push image:

```bash
aws ecr get-login-password --region ap-south-1 | docker login --username AWS --password-stdin <account>.dkr.ecr.ap-south-1.amazonaws.com
docker build -t hookrelay:dev .
docker tag hookrelay:dev <ecr_url>:dev
docker push <ecr_url>:dev
```

3. Terraform (create `terraform.tfvars` with secrets; never commit it):

```hcl
db_password = "..."
admin_token = "..."
image_tag   = "dev"
alert_email = "you@example.com"
```

```bash
cd terraform
terraform init
terraform plan
terraform apply
```

4. Verify (record date, ALB DNS, event count, costs):

```bash
ALB=$(terraform output -raw alb_dns_name)
curl -sf "http://$ALB/health"
# register endpoint, enqueue N events, force worker task stop mid-delivery
# confirm delivered / retried / DLQ via /v1/stats and /v1/dead_letters
```

5. Tear down:

```bash
terraform destroy
```

## Threat model (short)

- Untrusted webhook receivers; workers egress to the internet via NAT.
- Admin API is public on ALB HTTP today — put Cognito/OIDC or private auth in front before any shared use.
- DB credentials live in Secrets Manager; task role is execution-only for secret read + logs.
- No WAF, no mTLS, no multi-AZ RDS in this dev cut.

## Verification log

| Date | Env | Events | Result | Notes |
| --- | --- | ---: | --- | --- |
| _pending_ | | | | Fill after first real `apply` |
