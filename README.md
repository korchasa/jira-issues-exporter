# Jira Issues Exporter for Prometheus

Jira Issues Exporter for Prometheus is a specialized tool that extracts issues data from Jira and formats it for Prometheus monitoring. The primary goal is to provide teams with the ability to monitor project progress, workload distribution, and performance metrics through Prometheus and Grafana. This integration facilitates a seamless blend of project management insights with the power of observability tools.

```
jira_issues_count{assignee="alice@example.com",issueType="Epic",priority="",project="DEVOPS",status="TODO",statusCategory="To Do"} 1
jira_issues_count{assignee="alice@example.com",issueType="Task",priority="",project="DEVOPS",status="Aborted",statusCategory="Done"} 2
jira_issues_count{assignee="alice@example.com",issueType="Task",priority="",project="DEVOPS",status="Done",statusCategory="Done"} 2
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
- `jira_issues_count` - the number of issues in a given status (labels: `project`, `issueType`, `status`, `statusCategory`, `priority`, `assignee`)
- `jira_issue_time_in_status` - the time spent in a given status (labels: `project`, `issueType`, `priority`, `assignee`)
