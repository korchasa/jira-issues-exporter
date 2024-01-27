package main

import (
    "encoding/json"
    "fmt"
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promhttp"
    "net/http"
    "net/url"
    "os"
    "slices"
    "time"
)

const (
    jiraTimeFormat = "2006-01-02T15:04:05.000-0700"
)

type config struct {
    listen            string
    dataRefreshPeriod time.Duration
    jiraURL           string
    jiraUser          string
    jiraAPIToken      string
    projects          string
    analyzePeriodDays string
}

// fetchJiraData connects to the Jira API and fetches issues data
func fetchJiraData(cfg config) ([]JiraIssue, error) {
    issues := make([]JiraIssue, 0)
    startAt := 0
    for {
        issuesChunk, err := fetchStartingFrom(cfg, startAt)
        if err != nil {
            return nil, err
        }
        if len(issuesChunk) == 0 {
            break
        }
        issues = append(issues, issuesChunk...)
        startAt += len(issuesChunk)
    }
    return issues, nil
}

func fetchStartingFrom(cfg config, startAt int) ([]JiraIssue, error) {
    fmt.Printf("Fetching Jira data starting from %d\n", startAt)
    // Adjust the API URL based on your Jira setup
    jql := fmt.Sprintf("updated >= -%sd AND project in (%s)", cfg.analyzePeriodDays, cfg.projects)
    apiURL := fmt.Sprintf("%s/rest/api/3/search?expand=changelog&fields=created,status,assignee,project,issuetype&startAt=%d&jql=%s", cfg.jiraURL, startAt, url.QueryEscape(jql))
    fmt.Printf("Fetching %s\n", apiURL)

    // Create a new HTTP request
    req, err := http.NewRequest("GET", apiURL, nil)
    if err != nil {
        return nil, err
    }

    // Set authentication headers
    req.SetBasicAuth(cfg.jiraUser, cfg.jiraAPIToken)

    // Make the HTTP request
    client := &http.Client{}
    resp, err := client.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    // Check if the response is successful
    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("failed to fetch data: %s", resp.Status)
    }

    // Decode the JSON response
    var result struct {
        Issues []JiraIssue `json:"issues"`
    }
    if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
        return nil, err
    }

    return result.Issues, nil
}

// Define Prometheus metrics
var (
    jiraIssuesCount = prometheus.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "jira_issues_count",
            Help: "Count of Jira issues by various labels.",
        },
        []string{"project", "priority", "status", "statusCategory", "assignee", "issueType"},
    )
    jiraIssueTimeInStatus = prometheus.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "jira_issue_time_in_status",
            Help:    "Time spent by issues in each status.",
            Buckets: prometheus.ExponentialBuckets(1, 10, 8),
        },
        []string{"project", "priority", "assignee", "issueType"},
    )
)

func init() {
    // Register metrics with Prometheus
    prometheus.MustRegister(jiraIssuesCount)
    prometheus.MustRegister(jiraIssueTimeInStatus)
}

// JiraIssue represents the structure of an issue from Jira
type JiraIssue struct {
    Key       string `json:"key"`
    Changelog struct {
        Histories []struct {
            Created string `json:"created"`
            Items   []struct {
                Field      string      `json:"field"`
                FromString interface{} `json:"fromString"`
            } `json:"items"`
        } `json:"histories"`
    } `json:"changelog"`
    Fields struct {
        Created  string `json:"created"`
        Priority struct {
            Name string `json:"name"`
        } `json:"priority"`
        Assignee struct {
            EmailAddress string `json:"emailAddress"`
        } `json:"assignee"`
        Status struct {
            Name           string `json:"name"`
            StatusCategory struct {
                Name string `json:"name"`
            } `json:"statusCategory"`
        } `json:"status"`
        IssueType struct {
            Name string `json:"name"`
        } `json:"issuetype"`
        Project struct {
            Key string `json:"key"`
        } `json:"project"`
    } `json:"fields"`
}

// transformDataForPrometheus updates Prometheus metrics instead of returning a string
func transformDataForPrometheus(issue JiraIssue) {
    //fmt.Printf("Processing issue %s\n", issue.Key)
    jiraIssuesCount.With(prometheus.Labels{
        "project":        issue.Fields.Project.Key,
        "priority":       issue.Fields.Priority.Name,
        "status":         issue.Fields.Status.Name,
        "statusCategory": issue.Fields.Status.StatusCategory.Name,
        "assignee":       issue.Fields.Assignee.EmailAddress,
        "issueType":      issue.Fields.IssueType.Name,
    }).Inc()
    calculateStatusDurations(issue)
}

func calculateStatusDurations(issue JiraIssue) {
    statusDurations := make(map[string]time.Duration)

    slices.Reverse(issue.Changelog.Histories)
    statusChangeTime := mustTimeParse(issue.Fields.Created)
    for _, history := range issue.Changelog.Histories {
        changeTime := mustTimeParse(history.Created)
        for _, item := range history.Items {
            if item.Field == "status" {
                duration := changeTime.Sub(statusChangeTime)
                statusDurations[item.FromString.(string)] += duration
                statusChangeTime = changeTime
            }
        }
    }
    for _, duration := range statusDurations {
        //fmt.Printf("Issue %s spent %s in status %s\n", issue.Key, duration, status)
        jiraIssueTimeInStatus.With(prometheus.Labels{
            "project":   issue.Fields.Project.Key,
            "priority":  issue.Fields.Priority.Name,
            "assignee":  issue.Fields.Assignee.EmailAddress,
            "issueType": issue.Fields.IssueType.Name,
        }).Observe(duration.Seconds())
    }
}

// exposeMetrics serves the Prometheus metrics using promhttp
func exposeMetrics(cfg config) {
    http.Handle("/liveness", livenessHandler())
    http.Handle("/readiness", readinessHandler(cfg))
    http.Handle("/metrics", promhttp.Handler())
    fmt.Printf("Serving metrics on %s\n", cfg.listen)
    err := http.ListenAndServe(cfg.listen, nil)
    if err != nil {
        fmt.Println("Error starting HTTP server:", err)
    }
}

func livenessHandler() http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    })
}

func readinessHandler(cfg config) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        _, err := fetchStartingFrom(cfg, 0)
        if err != nil {
            fmt.Printf("Error fetching Jira data: %s\n", err)
            w.WriteHeader(http.StatusInternalServerError)
            return
        } else {
            w.WriteHeader(http.StatusOK)
        }
    })
}

func main() {
    var err error
    cfg := config{
        listen:            getEnvOrDie("LISTEN"),
        analyzePeriodDays: getEnvOrDefault("ANALYZE_PERIOD_DAYS", "90"),
        jiraURL:           getEnvOrDie("JIRA_URL"),
        jiraUser:          getEnvOrDie("JIRA_USER"),
        jiraAPIToken:      getEnvOrDie("JIRA_API_TOKEN"),
        projects:          getEnvOrDie("PROJECTS"),
    }
    cfg.dataRefreshPeriod, err = time.ParseDuration(getEnvOrDefault("DATA_REFRESH_PERIOD", "5m"))
    failOnError(err)
    if cfg.analyzePeriodDays == "" {
        cfg.analyzePeriodDays = "90"
    }

    // Repeat every cfg.dataRefreshPeriod and fetch Jira data
    go func() {
        for {
            now := time.Now()
            issues, err := fetchJiraData(cfg)
            if err != nil {
                fmt.Println("Error fetching Jira data:", err)
                return
            }
            for _, issue := range issues {
                transformDataForPrometheus(issue)
            }
            fmt.Printf("Fetched %d issues in %s\n", len(issues), time.Since(now))
            time.Sleep(cfg.dataRefreshPeriod)
        }
    }()

    exposeMetrics(cfg)
}

func getEnvOrDie(name string) string {
    value := os.Getenv(name)
    if value == "" {
        panic(fmt.Sprintf("%s env is empty", name))
    }
    return value
}

func getEnvOrDefault(name string, defaultValue string) string {
    value := os.Getenv(name)
    if value == "" {
        return defaultValue
    }
    return value
}

func failOnError(err error) {
    if err != nil {
        fmt.Printf("Error: %s", err)
        os.Exit(1)
    }
}

func mustTimeParse(str string) time.Time {
    t, err := time.Parse(jiraTimeFormat, str)
    if err != nil {
        panic(err)
    }
    return t
}
