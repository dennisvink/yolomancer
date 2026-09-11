# Agent Skills for AWS

This directory contains agent skills — curated packages of instructions and reference materials that help AI coding agents complete AWS tasks effectively. We plan to release new and updated skills on a regular cadence.

## Skill categories

Skills are organized into two categories: **core** and **specialized**.

### Core skills

Core skills are bundled with the [aws-core plugin](../plugins/aws-core/) and provide
broad guidance across the most commonly used AWS services and development patterns.
We recommend installing the aws-core plugin as your starting point if you're building
and operating applications on AWS. It gives your agent foundational knowledge about
service selection, architecture decisions, SDK usage, infrastructure-as-code, security,
observability, and cost management.

Core skills cover:

- AWS SDK usage patterns (Python, JavaScript, Swift)
- Infrastructure as code (CDK, CloudFormation)
- Compute (serverless, containers)
- Security and identity (IAM)
- Observability (CloudWatch, X-Ray, CloudTrail)
- Application integration (messaging, streaming)
- Cost management (Billing and Cost Management)
- Full-stack applictaion development (AWS Blocks)
- Generative AI (Bedrock)
- Databases (service selection and routing)

### Specialized skills

Specialized skills offer service-specific guidance and detailed workflows for common
tasks that agents struggle with. These go deeper than core skills — providing
step-by-step procedures for specific operations like creating a data lake table,
launching an EC2 instance with best practices, or troubleshooting EFS connectivity.

Install specialized skills when you're working in a specific domain and want your
agent to follow AWS-recommended procedures rather than improvising from general
knowledge.

Specialized skills are organized by AWS service category:

- **[Analytics](specialized-skills/analytics-skills/)**
- **[Database](specialized-skills/database-skills/)**
- **[EC2](specialized-skills/ec2-skills/)**
- **[End User Computing](specialized-skills/end-user-computing-skills/)**
- **[Messaging & Streaming](specialized-skills/messaging-and-streaming-skills/)**
- **[Migration & Modernization](specialized-skills/migration-and-modernization-skills/)**
- **[Networking & Content Delivery](specialized-skills/networking-and-content-delivery-skills/)**
- **[Operations](specialized-skills/operations-skills/)**
- **[Quantum Computing](specialized-skills/quantum-computing-skills/)**
- **[Resilience](specialized-skills/resilience-skills/)**
- **[Security & Identity](specialized-skills/security-and-identity-skills/)**
- **[Serverless](specialized-skills/serverless-skills/)**
- **[Storage](specialized-skills/storage-skills/)**
- **[Web & Mobile development](specialized-skills/aws-amplify/)**
