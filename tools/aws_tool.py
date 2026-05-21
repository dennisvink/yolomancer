def yolomancer_tool():
    actions = [
        "help",
        "sts.get_caller_identity",
        "s3.list_buckets",
        "s3.list_objects",
        "s3.create_bucket",
        "s3.delete_bucket",
        "iam.list_users",
        "iam.get_user",
        "ec2.describe_vpcs",
        "dynamodb.list_tables",
        "dynamodb.describe_table",
        "dynamodb.create_table",
        "dynamodb.delete_table",
        "cloudformation.list_stacks",
        "cloudformation.describe_stacks",
        "cloudformation.create_stack",
        "cloudformation.delete_stack",
        "route53.list_hosted_zones",
        "account.list_regions",
        "request",
    ]
    return {
        "name": "aws_tool",
        "description": "Use this tool to run operations on AWS. It can be used for STS identity checks, S3 buckets/objects, IAM users, EC2 VPCs, DynamoDB tables, CloudFormation stacks, Route53 hosted zones, Account regions, or a generic signed AWS request. Use action=\"help\" with arguments.service set to a service name, for example {\"action\":\"help\",\"arguments\":{\"service\":\"cloudformation\"}}, to look up available operations and invocation examples.",
        "parameters": {
            "type": "object",
            "properties": {
                "action": {
                    "type": "string",
                    "description": "AWS helper action to run.",
                    "enum": actions,
                },
                "arguments": {
                    "type": "object",
                    "description": "Action-specific arguments.",
                    "additionalProperties": True,
                },
            },
            "required": ["action"],
            "additionalProperties": False,
        },
    }


import yolomancer_aws as aws


AWS_HELP = {
    "all": {
        "summary": "AWS helper actions available through aws_tool.",
        "services": [
            "sts",
            "s3",
            "iam",
            "ec2",
            "dynamodb",
            "cloudformation",
            "route53",
            "account",
            "request",
        ],
        "example": {
            "action": "help",
            "arguments": {"service": "cloudformation"},
        },
    },
    "sts": {
        "summary": "Security Token Service helpers.",
        "operations": {
            "sts.get_caller_identity": {
                "scope": "read",
                "description": "Return the AWS identity currently used by AWS helper calls.",
                "arguments": {},
                "example": {"action": "sts.get_caller_identity", "arguments": {}},
            },
        },
    },
    "s3": {
        "summary": "S3 bucket and object helpers.",
        "operations": {
            "s3.list_buckets": {
                "scope": "read",
                "description": "List buckets visible to the configured AWS role.",
                "arguments": {},
                "example": {"action": "s3.list_buckets", "arguments": {}},
            },
            "s3.list_objects": {
                "scope": "read",
                "description": "List objects in one bucket, optionally under a prefix.",
                "arguments": {"bucket": "required string", "prefix": "optional string"},
                "example": {
                    "action": "s3.list_objects",
                    "arguments": {"bucket": "my-bucket", "prefix": "logs/"},
                },
            },
            "s3.create_bucket": {
                "scope": "write",
                "description": "Create an S3 bucket.",
                "arguments": {"bucket": "required string"},
                "example": {"action": "s3.create_bucket", "arguments": {"bucket": "my-bucket"}},
            },
            "s3.delete_bucket": {
                "scope": "destructive",
                "description": "Delete an empty S3 bucket.",
                "arguments": {"bucket": "required string"},
                "example": {"action": "s3.delete_bucket", "arguments": {"bucket": "my-bucket"}},
            },
        },
    },
    "iam": {
        "summary": "IAM user inspection helpers.",
        "operations": {
            "iam.list_users": {
                "scope": "read",
                "description": "List IAM users.",
                "arguments": {},
                "example": {"action": "iam.list_users", "arguments": {}},
            },
            "iam.get_user": {
                "scope": "read",
                "description": "Get one IAM user, or the current IAM user if user_name is omitted.",
                "arguments": {"user_name": "optional string"},
                "example": {"action": "iam.get_user", "arguments": {"user_name": "alice"}},
            },
        },
    },
    "ec2": {
        "summary": "EC2 networking helpers.",
        "operations": {
            "ec2.describe_vpcs": {
                "scope": "read",
                "description": "Describe VPCs in the configured region.",
                "arguments": {},
                "example": {"action": "ec2.describe_vpcs", "arguments": {}},
            },
        },
    },
    "dynamodb": {
        "summary": "DynamoDB table helpers.",
        "operations": {
            "dynamodb.list_tables": {
                "scope": "read",
                "description": "List DynamoDB table names.",
                "arguments": {},
                "example": {"action": "dynamodb.list_tables", "arguments": {}},
            },
            "dynamodb.describe_table": {
                "scope": "read",
                "description": "Describe one DynamoDB table.",
                "arguments": {"table_name": "required string"},
                "example": {
                    "action": "dynamodb.describe_table",
                    "arguments": {"table_name": "WorkshopTable"},
                },
            },
            "dynamodb.create_table": {
                "scope": "write",
                "description": "Create a pay-per-request table with a string partition key.",
                "arguments": {
                    "table_name": "required string",
                    "partition_key": "optional string, defaults to id",
                },
                "example": {
                    "action": "dynamodb.create_table",
                    "arguments": {"table_name": "WorkshopTable", "partition_key": "id"},
                },
            },
            "dynamodb.delete_table": {
                "scope": "destructive",
                "description": "Delete one DynamoDB table.",
                "arguments": {"table_name": "required string"},
                "example": {
                    "action": "dynamodb.delete_table",
                    "arguments": {"table_name": "WorkshopTable"},
                },
            },
        },
    },
    "cloudformation": {
        "summary": "CloudFormation stack helpers.",
        "operations": {
            "cloudformation.list_stacks": {
                "scope": "read",
                "description": "List CloudFormation stack summaries.",
                "arguments": {},
                "example": {"action": "cloudformation.list_stacks", "arguments": {}},
            },
            "cloudformation.describe_stacks": {
                "scope": "read",
                "description": "Describe all stacks or one named stack.",
                "arguments": {"stack_name": "optional string"},
                "example": {
                    "action": "cloudformation.describe_stacks",
                    "arguments": {"stack_name": "workshop-demo"},
                },
            },
            "cloudformation.create_stack": {
                "scope": "write",
                "description": "Create a stack from a template body.",
                "arguments": {
                    "stack_name": "required string",
                    "template_body": "required CloudFormation template string",
                    "capabilities": "optional list of CAPABILITY_IAM, CAPABILITY_NAMED_IAM, CAPABILITY_AUTO_EXPAND",
                },
                "example": {
                    "action": "cloudformation.create_stack",
                    "arguments": {
                        "stack_name": "workshop-demo",
                        "template_body": "{\"AWSTemplateFormatVersion\":\"2010-09-09\",\"Resources\":{}}",
                        "capabilities": [],
                    },
                },
            },
            "cloudformation.delete_stack": {
                "scope": "destructive",
                "description": "Delete one CloudFormation stack.",
                "arguments": {"stack_name": "required string"},
                "example": {
                    "action": "cloudformation.delete_stack",
                    "arguments": {"stack_name": "workshop-demo"},
                },
            },
        },
    },
    "route53": {
        "summary": "Route53 hosted zone helpers.",
        "operations": {
            "route53.list_hosted_zones": {
                "scope": "read",
                "description": "List hosted zones.",
                "arguments": {},
                "example": {"action": "route53.list_hosted_zones", "arguments": {}},
            },
        },
    },
    "account": {
        "summary": "AWS Account helpers.",
        "operations": {
            "account.list_regions": {
                "scope": "read",
                "description": "List account regions and opt-in status.",
                "arguments": {},
                "example": {"action": "account.list_regions", "arguments": {}},
            },
        },
    },
    "request": {
        "summary": "Generic signed AWS HTTPS request escape hatch.",
        "operations": {
            "request": {
                "scope": "unknown",
                "description": "Sign and send an AWS HTTPS request. Prefer service-specific actions when available.",
                "arguments": {
                    "service": "required AWS signing service, for example s3",
                    "method": "required HTTP method",
                    "url": "required https URL",
                    "body": "optional string or JSON value",
                    "headers": "optional object",
                    "region": "optional signing region",
                },
                "example": {
                    "action": "request",
                    "arguments": {
                        "service": "s3",
                        "method": "GET",
                        "url": "https://s3.amazonaws.com/",
                        "headers": {},
                    },
                },
            },
        },
    },
}


def _args(args):
    return args.get("arguments") or {}


def _help(params):
    service = (params.get("service") or params.get("topic") or "all").lower()
    if service in AWS_HELP:
        return {
            "ok": True,
            "service": service,
            "help": AWS_HELP[service],
        }
    return {
        "ok": False,
        "error": "unknown AWS help service",
        "service": service,
        "available_services": sorted(AWS_HELP.keys()),
    }


def run(args):
    action = args.get("action")
    params = _args(args)

    if action == "help":
        return _help(params)

    if action == "sts.get_caller_identity":
        return aws.sts.get_caller_identity()

    if action == "s3.list_buckets":
        return aws.s3.list_buckets()
    if action == "s3.list_objects":
        return aws.s3.list_objects(
            bucket=params.get("bucket"),
            prefix=params.get("prefix"),
        )
    if action == "s3.create_bucket":
        return aws.s3.create_bucket(bucket=params.get("bucket"))
    if action == "s3.delete_bucket":
        return aws.s3.delete_bucket(bucket=params.get("bucket"))

    if action == "iam.list_users":
        return aws.iam.list_users()
    if action == "iam.get_user":
        return aws.iam.get_user(user_name=params.get("user_name"))

    if action == "ec2.describe_vpcs":
        return aws.ec2.describe_vpcs()

    if action == "dynamodb.list_tables":
        return aws.dynamodb.list_tables()
    if action == "dynamodb.describe_table":
        return aws.dynamodb.describe_table(table_name=params.get("table_name"))
    if action == "dynamodb.create_table":
        return aws.dynamodb.create_table(
            table_name=params.get("table_name"),
            partition_key=params.get("partition_key", "id"),
        )
    if action == "dynamodb.delete_table":
        return aws.dynamodb.delete_table(table_name=params.get("table_name"))

    if action == "cloudformation.list_stacks":
        return aws.cloudformation.list_stacks()
    if action == "cloudformation.describe_stacks":
        return aws.cloudformation.describe_stacks(stack_name=params.get("stack_name"))
    if action == "cloudformation.create_stack":
        return aws.cloudformation.create_stack(
            stack_name=params.get("stack_name"),
            template_body=params.get("template_body"),
            capabilities=params.get("capabilities"),
        )
    if action == "cloudformation.delete_stack":
        return aws.cloudformation.delete_stack(stack_name=params.get("stack_name"))

    if action == "route53.list_hosted_zones":
        return aws.route53.list_hosted_zones()

    if action == "account.list_regions":
        return aws.account.list_regions()

    if action == "request":
        return aws.request(
            service=params.get("service"),
            method=params.get("method"),
            url=params.get("url"),
            body=params.get("body", ""),
            headers=params.get("headers"),
            region=params.get("region"),
        )

    return {
        "ok": False,
        "error": "unsupported AWS action",
        "action": action,
    }
