# Jira Issues Exporter for Prometheus

Jira Issues Exporter for Prometheus is a specialized tool that extracts issues data from Jira and formats it for Prometheus monitoring. The primary goal is to provide teams with the ability to monitor project progress, workload distribution, and performance metrics through Prometheus and Grafana. This integration facilitates a seamless blend of project management insights with the power of observability tools.

```
jira_issue_count{assignee="alice@example.com",issueType="Epic",priority="",project="DEVOPS",status="TODO",statusCategory="To Do"} 1
jira_issue_count{assignee="alice@example.com",issueType="Task",priority="",project="DEVOPS",status="Aborted",statusCategory="Done"} 2
jira_issue_count{assignee="alice@example.com",issueType="Task",priority="",project="DEVOPS",status="Done",statusCategory="Done"} 2
jira_issue_time_in_status_bucket{assignee="bob@example.com",issueType="Sub-task",priority="",project="DEVOPS",le="10000"} 0
jira_issue_time_in_status_bucket{assignee="bob@example.com",issueType="Sub-task",priority="",project="DEVOPS",le="100000"} 1
jira_issue_time_in_status_bucket{assignee="bob@example.com",issueType="Sub-task",priority="",project="DEVOPS",le="1e+06"} 1
jira_issue_time_in_status_bucket{assignee="bob@example.com",issueType="Sub-task",priority="",project="DEVOPS",le="1e+07"} 1
jira_issue_time_in_status_bucket{assignee="bob@example.com",issueType="Sub-task",priority="",project="DEVOPS",le="+Inf"} 1
jira_issue_time_in_status_sum{assignee="bob@example.com",issueType="Sub-task",
...
```

## Metrics

The exporter provides the following metrics:
- `jira_issue_count` - the number of issues in a given status (labels: `project`, `issueType`, `status`, `statusCategory`, `priority`, `assignee`)
- `jira_issue_time_in_status` - the time spent in a given status (labels: `project`, `issueType`, `priority`, `assignee`)

## Configuration

The exporter is configured via environment variables:

| Variable              | Description                                      |
|-----------------------|--------------------------------------------------|
| `LISTEN`              | Address to listen                                |
| `JIRA_URL`            | Jira URL                                         |
| `JIRA_USER`           | Jira username                                    |
| `JIRA_API_TOKEN`      | Jira API token                                   |
| `JIRA_PROJECTS`       | Comma-separated list of Jira projects to monitor |
| `ANALYZE_PERIOD_DAYS` | Number of days to analyze (default: `90`)        |
| `DATA_REFRESH_PERIOD` | Data refresh period in seconds (default: `5m`)   |


## Todo

- do not reset the metrics on each scrape
- add statuses to the jira_issue_time_in_status metric
- strange issues without assignee
- test on big projects
