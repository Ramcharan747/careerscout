# CareerScout — Terraform Infrastructure (Team 8)
# AWS ECS Graviton4 ARM Spot — Primary cluster for all Go services

terraform {
  required_version = ">= 1.6"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }

  # State stored in S3 + DynamoDB lock
  backend "s3" {
    bucket         = "careerscout-terraform-state"
    key            = "prod/terraform.tfstate"
    region         = "us-east-1"
    dynamodb_table = "careerscout-terraform-locks"
    encrypt        = true
  }
}

provider "aws" {
  region = var.aws_region
}

# ── Variables ─────────────────────────────────────────────────────────────────

variable "aws_region" {
  default = "us-east-1"
}

variable "environment" {
  default = "prod"
}

variable "vpc_id" {
  description = "VPC ID for ECS tasks"
}

variable "private_subnet_ids" {
  type        = list(string)
  description = "Private subnets for ECS tasks"
}

# ── ECS Cluster ───────────────────────────────────────────────────────────────

resource "aws_ecs_cluster" "careerscout" {
  name = "careerscout-${var.environment}"

  setting {
    name  = "containerInsights"
    value = "enabled"
  }

  tags = {
    Project     = "CareerScout"
    Environment = var.environment
  }
}

resource "aws_ecs_cluster_capacity_providers" "careerscout" {
  cluster_name       = aws_ecs_cluster.careerscout.name
  capacity_providers = ["FARGATE_SPOT", "FARGATE"]

  default_capacity_provider_strategy {
    capacity_provider = "FARGATE_SPOT"
    weight            = 80
    base              = 0
  }

  default_capacity_provider_strategy {
    capacity_provider = "FARGATE"
    weight            = 20
    base              = 1 # always one on-demand to handle SIGTERM gracefully
  }
}

# ── ECS Task Definitions ──────────────────────────────────────────────────────
# Each service is a separate ECS task on Graviton4 ARM (arm64)

locals {
  services = {
    ingestion  = { cpu = 512,  memory = 1024 }
    tier1      = { cpu = 1024, memory = 2048 }
    tier2      = { cpu = 4096, memory = 8192 }  # CDP workers need more
    normalise  = { cpu = 512,  memory = 1024 }
  }
}

resource "aws_ecs_task_definition" "services" {
  for_each = local.services

  family                   = "careerscout-${each.key}"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = each.value.cpu
  memory                   = each.value.memory
  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "ARM64"  # Graviton4
  }
  execution_role_arn = aws_iam_role.ecs_execution.arn
  task_role_arn      = aws_iam_role.ecs_task.arn

  container_definitions = jsonencode([
    {
      name      = each.key
      image     = "${aws_ecr_repository.services[each.key].repository_url}:latest"
      essential = true

      environment = [
        { name = "REDPANDA_BROKERS", value = var.redpanda_brokers },
        { name = "LOG_LEVEL",        value = "info" }
      ]

      secrets = [
        { name = "DATABASE_URL", valueFrom = aws_ssm_parameter.database_url.arn }
      ]

      logConfiguration = {
        logDriver = "awslogs"
        options = {
          "awslogs-group"         = "/ecs/careerscout/${each.key}"
          "awslogs-region"        = var.aws_region
          "awslogs-stream-prefix" = "ecs"
        }
      }

      # Hard memory limit per container (not just the task limit)
      memoryReservation = each.value.memory
    }
  ])
}

# ── ECR Repositories ──────────────────────────────────────────────────────────

resource "aws_ecr_repository" "services" {
  for_each             = local.services
  name                 = "careerscout/${each.key}"
  image_tag_mutability = "MUTABLE"

  image_scanning_configuration {
    scan_on_push = true
  }
}

# ── ECS Services ──────────────────────────────────────────────────────────────

resource "aws_ecs_service" "services" {
  for_each = local.services

  name            = "careerscout-${each.key}"
  cluster         = aws_ecs_cluster.careerscout.id
  task_definition = aws_ecs_task_definition.services[each.key].arn
  desired_count   = 2  # minimum 2 for redundancy

  capacity_provider_strategy {
    capacity_provider = "FARGATE_SPOT"
    weight            = 80
  }
  capacity_provider_strategy {
    capacity_provider = "FARGATE"
    weight            = 20
    base              = 1
  }

  network_configuration {
    subnets          = var.private_subnet_ids
    security_groups  = [aws_security_group.ecs_tasks.id]
    assign_public_ip = false
  }

  # Enable graceful Spot interruption handling
  enable_execute_command = false

  lifecycle {
    ignore_changes = [desired_count] # managed by auto-scaling
  }
}

# ── Auto-scaling ──────────────────────────────────────────────────────────────

resource "aws_appautoscaling_target" "tier2" {
  max_capacity       = 50
  min_capacity       = 2
  resource_id        = "service/${aws_ecs_cluster.careerscout.name}/careerscout-tier2"
  scalable_dimension = "ecs:service:DesiredCount"
  service_namespace  = "ecs"
}

resource "aws_appautoscaling_policy" "tier2_cpu" {
  name               = "careerscout-tier2-cpu"
  policy_type        = "TargetTrackingScaling"
  resource_id        = aws_appautoscaling_target.tier2.resource_id
  scalable_dimension = aws_appautoscaling_target.tier2.scalable_dimension
  service_namespace  = aws_appautoscaling_target.tier2.service_namespace

  target_tracking_scaling_policy_configuration {
    predefined_metric_specification {
      predefined_metric_type = "ECSServiceAverageCPUUtilization"
    }
    target_value = 70.0
  }
}

# ── Security Groups ───────────────────────────────────────────────────────────

resource "aws_security_group" "ecs_tasks" {
  name        = "careerscout-ecs-tasks"
  description = "Allow ECS tasks to reach Redpanda, Postgres, and internet"
  vpc_id      = var.vpc_id

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

# ── CloudWatch Log Groups ─────────────────────────────────────────────────────

resource "aws_cloudwatch_log_group" "services" {
  for_each          = local.services
  name              = "/ecs/careerscout/${each.key}"
  retention_in_days = 14
}

# ── SSM Parameters (secrets) ──────────────────────────────────────────────────

resource "aws_ssm_parameter" "database_url" {
  name  = "/careerscout/prod/DATABASE_URL"
  type  = "SecureString"
  value = "PLACEHOLDER_SET_VIA_CLI"  # Set manually: aws ssm put-parameter ...

  lifecycle {
    ignore_changes = [value]
  }
}

# ── Variables used in task definitions ───────────────────────────────────────

variable "redpanda_brokers" {
  description = "Comma-separated Redpanda broker list"
}

